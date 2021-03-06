package _import

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/IBM/go-sdk-core/v4/core"
	rcv2 "github.com/IBM/platform-services-go-sdk/resourcecontrollerv2"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

	"github.com/ppc64le-cloud/pvsadm/pkg"
	"github.com/ppc64le-cloud/pvsadm/pkg/client"
	"github.com/ppc64le-cloud/pvsadm/pkg/utils"
)

var Cmd = &cobra.Command{
	Use:   "import",
	Short: "Import the image into PowerVS instances",
	Long: `Import the image into PowerVS instances
pvsadm image import --help for information

# Set the API key or feed the --api-key commandline argument
export IBMCLOUD_API_KEY=<IBM_CLOUD_API_KEY>

Examples:

# import image using default storage type (service credential will be autogenerated)
pvsadm image import -n upstream-core-lon04 -b <BUCKETNAME> --object-name rhel-83-10032020.ova.gz --image-name test-image -r <REGION>

# import image using default storage type with specifying the accesskey and secretkey explicitly
pvsadm image import -n upstream-core-lon04 -b <BUCKETNAME> --accesskey <ACCESSKEY> --secretkey <SECRETKEY> --object-name rhel-83-10032020.ova.gz --image-name test-image -r <REGION>

# with user provided storage type
pvsadm image import -n upstream-core-lon04 -b <BUCKETNAME> -r <REGION> --storagetype <STORAGETYPE> --object-name rhel-83-10032020.ova.gz --image-name test-image -r <REGION>

# If user wants to specify the type of OS
pvsadm image import -n upstream-core-lon04 -b <BUCKETNAME> --object-name rhel-83-10032020.ova.gz --image-name test-image --ostype <OSTYPE> -r <REGION>
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var s3client *client.S3Client
		opt := pkg.ImageCMDOptions
		apikey := pkg.Options.APIKey
		//validate inputs
		validOsType := []string{"aix", "ibmi", "redhat", "sles"}
		validStorageType := []string{"tier3", "tier1"}

		if opt.OsType != "" && !utils.Contains(validOsType, strings.ToLower(opt.OsType)) {
			klog.Errorf("Provide valid OsType.. allowable values are [aix, ibmi, redhat, sles]")
			os.Exit(1)
		}

		if !utils.Contains(validStorageType, strings.ToLower(opt.StorageType)) {
			klog.Errorf("Provide valid StorageType.. allowable values are [tier1, tier3]")
			os.Exit(1)
		}

		bxCli, err := client.NewClient(apikey)
		if err != nil {
			return err
		}

		auth, err := core.NewIamAuthenticator(apikey, "", "", "", false, nil)
		if err != nil {
			return err
		}

		resourceController, err := client.NewResourceControllerV2(&rcv2.ResourceControllerV2Options{
			Authenticator: auth,
		})
		if err != nil {
			return err
		}

		instances, _, err := resourceController.ResourceControllerV2.ListResourceInstances(resourceController.ResourceControllerV2.NewListResourceInstancesOptions().SetType("service_instance"))
		if err != nil {
			return err
		}

		// Step 1: Find where COS for the bucket
		cosOfBucket := func(resources []rcv2.ResourceInstance) *rcv2.ResourceInstance {
			for _, resource := range resources {
				if strings.Contains(*resource.Crn, "cloud-object-storage") {
					s3client, err = client.NewS3Client(bxCli, *resource.Name, opt.Region)
					if err != nil {
						continue
					}
					buckets, err := s3client.S3Session.ListBuckets(nil)
					if err != nil {
						continue
					}
					for _, bucket := range buckets.Buckets {
						if *bucket.Name == opt.BucketName {
							return &resource
						}
					}
				}
			}
			return nil
		}(instances.Resources)

		if cosOfBucket == nil {
			return fmt.Errorf("failed to find the COS instance for the bucket mentioned: %s", opt.BucketName)
		}
		klog.Infof("%s bucket found in the %s[ID:%s] COS instance", opt.BucketName, *cosOfBucket.Name, *cosOfBucket.ID)

		//Step 2: Check if s3 object exists
		objectExists := s3client.CheckIfObjectExists(opt.BucketName, opt.ImageFilename)
		if !objectExists {
			return fmt.Errorf("failed to found the object %s in %s bucket", opt.ImageFilename, opt.BucketName)
		}
		klog.Infof("%s object found in the %s bucket\n", opt.ImageFilename, opt.BucketName)

		if opt.AccessKey == "" || opt.SecretKey == "" {
			// Step 3: Check if Service Credential exists for the found COS instance
			keys, _, err := resourceController.ResourceControllerV2.ListResourceKeys(resourceController.ResourceControllerV2.NewListResourceKeysOptions().SetName(opt.ServiceCredName))
			if err != nil {
				return fmt.Errorf("failed to list the service credentials: %v", err)
			}

			cred := new(rcv2.Credentials)
			if len(keys.Resources) == 0 {
				// Create the service credential if does not exist
				klog.Infof("Auto Generating the COS Service credential for importing the image with name: %s", opt.ServiceCredName)
				createResourceKeyOptions := &client.CreateResourceKeyOptions{
					CreateResourceKeyOptions: resourceController.ResourceControllerV2.NewCreateResourceKeyOptions(opt.ServiceCredName, *cosOfBucket.ID),
					Parameters:               map[string]interface{}{"HMAC": true},
				}

				key, _, err := resourceController.CreateResourceKey(createResourceKeyOptions)
				if err != nil {
					return err
				}
				cred = key.Credentials

			} else {
				// Use the service credential already created
				klog.Infof("Reading the existing service credential: %s", opt.ServiceCredName)
				cred = keys.Resources[0].Credentials
			}

			jsonString, err := json.Marshal(cred.GetProperty("cos_hmac_keys"))
			if err != nil {
				return err
			}
			h := struct {
				AccessKeyID string `json:"access_key_id"`
				SecretKeyID string `json:"secret_access_key"`
			}{}
			err = json.Unmarshal(jsonString, &h)
			if err != nil {
				klog.Errorf("failed to unmarshal the access credentials from the auto generated service credential")
				return err
			}

			// Step 4: Assign the Access Key and Secret Key for further operation
			opt.AccessKey = h.AccessKeyID
			opt.SecretKey = h.SecretKeyID

		}

		pvmclient, err := client.NewPVMClient(bxCli, opt.InstanceID, opt.InstanceName)
		if err != nil {
			return err
		}

		image, err := pvmclient.ImgClient.ImportImage(pvmclient.InstanceID, opt.ImageName, opt.ImageFilename, opt.Region,
			opt.AccessKey, opt.SecretKey, opt.BucketName, strings.ToLower(opt.OsType), strings.ToLower(opt.StorageType))
		if err != nil {
			return err
		}

		klog.Infof("Importing Image %s is currently in %s state, Please check the Progress in the IBM Cloud UI\n", *image.Name, image.State)
		return nil
	},
}

func init() {
	Cmd.Flags().StringVarP(&pkg.ImageCMDOptions.InstanceName, "instance-name", "n", "", "Instance name of the PowerVS")
	Cmd.Flags().StringVarP(&pkg.ImageCMDOptions.InstanceID, "instance-id", "i", "", "Instance ID of the PowerVS instance")
	Cmd.Flags().StringVarP(&pkg.ImageCMDOptions.BucketName, "bucket", "b", "", "Cloud Storage bucket name")
	Cmd.Flags().StringVarP(&pkg.ImageCMDOptions.Region, "region", "r", "", "COS bucket location")
	Cmd.Flags().StringVarP(&pkg.ImageCMDOptions.ImageFilename, "object-name", "o", "", "Cloud Storage image filename")
	Cmd.Flags().StringVar(&pkg.ImageCMDOptions.AccessKey, "accesskey", "", "Cloud Storage access key")
	Cmd.Flags().StringVar(&pkg.ImageCMDOptions.SecretKey, "secretkey", "", "Cloud Storage secret key")
	Cmd.Flags().StringVar(&pkg.ImageCMDOptions.ImageName, "image-name", "", "Name to give imported image")
	Cmd.Flags().StringVar(&pkg.ImageCMDOptions.OsType, "ostype", "redhat", "Image OS Type, accepted values are[aix, ibmi, redhat, sles]")
	Cmd.Flags().StringVar(&pkg.ImageCMDOptions.StorageType, "storagetype", "tier3", "Storage type, accepted values are [tier1, tier3]")
	Cmd.Flags().StringVar(&pkg.ImageCMDOptions.ServiceCredName, "service-credential-name", "pvsadm-service-cred", "Service Credential name to be auto generated")

	_ = Cmd.MarkFlagRequired("bucket")
	_ = Cmd.MarkFlagRequired("image-name")
	_ = Cmd.MarkFlagRequired("object-name")
	_ = Cmd.MarkFlagRequired("region")
}
