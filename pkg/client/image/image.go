package image

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/IBM-Cloud/power-go-client/clients/instance"
	"github.com/IBM-Cloud/power-go-client/ibmpisession"
	"github.com/IBM-Cloud/power-go-client/power/client/p_cloud_images"
	"github.com/IBM-Cloud/power-go-client/power/models"
	"github.com/ppc64le-cloud/pvsadm/pkg"
	"k8s.io/klog/v2"
)

type Client struct {
	session    *ibmpisession.IBMPISession
	client     *instance.IBMPIImageClient
	instanceID string
}

func NewClient(sess *ibmpisession.IBMPISession, powerinstanceid string) *Client {
	c := &Client{
		session:    sess,
		instanceID: powerinstanceid,
	}
	c.client = instance.NewIBMPIImageClient(sess, powerinstanceid)
	return c
}

func (c *Client) Get(id string) (*models.Image, error) {
	return c.client.Get(id, c.instanceID)
}

func (c *Client) GetAll() (*models.Images, error) {
	return c.client.GetAll(c.instanceID)
}

func (c *Client) Delete(id string) error {
	return c.client.Delete(id, c.instanceID)
}

//func ImportImage imports image from S3 Instance
func (c *Client) ImportImage(instanceID, imageName, s3Filename, region, accessKey, secretKey, bucketName, osType, storageType string) (*models.Image, error) {
	var source = "url"
	var body = models.CreateImage{
		ImageName:     imageName,
		ImageFilename: s3Filename,
		Region:        region,
		AccessKey:     accessKey,
		SecretKey:     secretKey,
		BucketName:    bucketName,
		OsType:        osType,
		DiskType:      storageType,
		Source:        &source,
	}

	params := p_cloud_images.NewPcloudCloudinstancesImagesPostParamsWithTimeout(pkg.TIMEOUT).WithCloudInstanceID(instanceID).WithBody(&body)
	resp1, resp2, err := c.session.Power.PCloudImages.PcloudCloudinstancesImagesPost(params, ibmpisession.NewAuth(c.session, instanceID))

	if err != nil {
		return nil, err
	}

	if resp1 != nil {
		klog.Errorf("Failed to intiate the import job")
		return nil, errors.New("Failed to initiate the import job")
	}

	if resp2.Payload.State == "queued" {
		klog.Infof("Post is successful %s", *resp2.Payload.ImageID)
	}

	return resp2.Payload, nil
}

func (c *Client) GetAllPurgeable(before, since time.Duration, expr string) ([]*models.ImageReference, error) {
	images, err := c.GetAll()
	if err != nil {
		return nil, fmt.Errorf("failed to get the list of instances: %v", err)
	}

	var candidates []*models.ImageReference
	for _, image := range images.Images {
		if expr != "" {
			if r, _ := regexp.Compile(expr); !r.MatchString(*image.Name) {
				continue
			}
		}
		if !pkg.IsPurgeable(time.Time(*image.CreationDate), before, since) {
			continue
		}
		candidates = append(candidates, image)
	}
	return candidates, nil
}
