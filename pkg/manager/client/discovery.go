package client

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/Jeffail/gabs/v2"
	"github.com/sirupsen/logrus"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

const (
	discoveryBurst = 10000
	discoveryQPS   = 10000
)

type ExcludeFilter func(schema.GroupVersion, metav1.APIResource) bool

type DiscoveryClient struct {
	Context         context.Context
	discoveryClient *discovery.DiscoveryClient
}

func NewDiscoveryClient(ctx context.Context, config *rest.Config) (*DiscoveryClient, error) {
	newConfig := rest.CopyConfig(config)
	newConfig.Burst = discoveryBurst
	newConfig.QPS = discoveryQPS

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(newConfig)
	if err != nil {
		return nil, err
	}

	return &DiscoveryClient{
		Context:         ctx,
		discoveryClient: discoveryClient,
	}, nil
}

func toObjCommon(b []byte, groupVersion, kind string) (*gabs.Container, error) {
	re := regexp.MustCompile(`("[a-zA-Z]+":)(null,)`)
	replaceString := re.ReplaceAllString(string(b), "$1\"null\",")

	re = regexp.MustCompile(`(\\"[a-zA-Z]+\\":)(null,)`)
	replaceString = re.ReplaceAllString(replaceString, "$1\\\"null\\\",")

	finalString := strings.ReplaceAll(replaceString, `""`, `"null"`)
	jsonParsed, err := gabs.ParseJSON([]byte(finalString))
	if err != nil {
		logrus.Errorf("Unable to parse json: %s, %s", groupVersion, kind)
		return nil, err
	}
	// the yaml contains a list of resources
	if _, err = jsonParsed.SetP("List", "kind"); err != nil {
		logrus.Error("Unable to set kind for list.")
		return nil, err
	}

	if _, err = jsonParsed.SetP("v1", "apiVersion"); err != nil {
		logrus.Error("Unable to set apiVersion for list.")
		return nil, err
	}

	for _, child := range jsonParsed.S("items").Children() {
		if _, err = child.SetP(groupVersion, "apiVersion"); err != nil {
			logrus.Error("Unable to set apiVersion field.")
			return nil, err
		}

		if _, err = child.SetP(strings.Title(kind), "kind"); err != nil {
			logrus.Error("Unable to set kind field.")
			return nil, err
		}
	}

	if kind == "Secret" {
		for _, child := range jsonParsed.S("items").Children() {
			if exists := child.Exists("data"); exists {
				_, err := child.SetP("", "data")
				if err != nil {
					logrus.Error("Unable to clear data section")
				}
			}
		}
	}
	return jsonParsed, nil
}
func toObjExtraModule(extraModule, resource string, b []byte, groupVersion, kind string) (interface{}, error) {
	jsonParsed, err := toObjCommon(b, groupVersion, kind)
	if err != nil {
		return nil, err
	}

	switch extraModule {
	case "Harvester":
		if err := toObjHarvesterExtra(jsonParsed, resource); err != nil {
			logrus.Error("Do extraParsed failure")
		}
	default:
		// no extra parser here
	}
	return jsonParsed, nil
}

func toObjHarvesterExtra(jsonParsed *gabs.Container, resource string) error {
	switch resource {
	case "secrets":
		logrus.Infof("Prepare to extra parsring for `secrets`\n")
	default:
		// undefined resource operation
	}
	return nil
}

func toObj(b []byte, groupVersion, kind string) (interface{}, error) {
	jsonParsed, err := toObjCommon(b, groupVersion, kind)
	if err != nil {
		return nil, err
	}

	return jsonParsed.Data(), nil
}

// Get extra resource/namespace and try to do specific filter with module name
func (dc *DiscoveryClient) SpecificResourcesForNamespace(moduleName string, extraResources map[string][]string, errLog io.Writer) (map[string]interface{}, error) {
	objs := make(map[string]interface{})

	lists, err := dc.discoveryClient.ServerPreferredResources()
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("/api/v1/namespaces/flee-local/secrets")

	result := dc.discoveryClient.RESTClient().Get().AbsPath(url).Do(dc.Context)

	// It is likely that errors can occur.
	if result.Error() != nil {
		logrus.Tracef("Failed to get %s: %v", url, result.Error())
		fmt.Fprintf(errLog, "Failed to get %s: %v\n", url, result.Error())
	}
	logrus.Infof("[DEBUG]: Get secret good!\n")

	for _, list := range lists {
		if len(list.APIResources) == 0 {
			continue
		}
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			continue
		}

		for _, resource := range list.APIResources {
			logrus.Infof("[DEBUG]: prepare to checking specific resource: %s", resource.Name)
			if !resource.Namespaced {
				continue
			}

			if namespaceList, found := extraResources[resource.Name]; found {
				logrus.Infof("[DEBUG]: found!, resource: %s, NS: %v", resource.Name, namespaceList)
				for _, namespace := range namespaceList {
					prefix := "apis"
					if gv.String() == "v1" {
						prefix = "api"
					}

					url := fmt.Sprintf("/%s/%s/namespaces/%s/%s", prefix, gv.String(), namespace, resource.Name)

					result := dc.discoveryClient.RESTClient().Get().AbsPath(url).Do(dc.Context)

					// It is likely that errors can occur.
					if result.Error() != nil {
						logrus.Tracef("Failed to get %s: %v", url, result.Error())
						fmt.Fprintf(errLog, "Failed to get %s: %v\n", url, result.Error())
						continue
					}

					// This produces a byte array of json.
					b, err := result.Raw()

					if err == nil {
						obj, err := toObjExtraModule(moduleName, resource.Name, b, gv.String(), resource.Kind)
						if err != nil {
							return nil, err
						}
						objs[gv.String()+"/"+resource.Name] = obj
					}
				}
			} else {
				continue
			}
		}
	}

	return objs, nil
}

// Get all the namespaced resources for a given namespace
func (dc *DiscoveryClient) ResourcesForNamespace(namespace string, exclude ExcludeFilter, errLog io.Writer) (map[string]interface{}, error) {
	objs := make(map[string]interface{})

	lists, err := dc.discoveryClient.ServerPreferredResources()
	if err != nil {
		return nil, err
	}

	for _, list := range lists {
		if len(list.APIResources) == 0 {
			continue
		}
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			continue
		}

		for _, resource := range list.APIResources {
			if !resource.Namespaced {
				continue
			}

			if exclude(gv, resource) {
				continue
			}

			// I would like to build the URL with rest client
			// methods, but I was not able to.  It might be
			// possible if a new rest client is created each
			// time with the GroupVersion
			prefix := "apis"
			if gv.String() == "v1" {
				prefix = "api"
			}
			url := fmt.Sprintf("/%s/%s/namespaces/%s/%s", prefix, gv.String(), namespace, resource.Name)

			result := dc.discoveryClient.RESTClient().Get().AbsPath(url).Do(dc.Context)

			// It is likely that errors can occur.
			if result.Error() != nil {
				logrus.Tracef("Failed to get %s: %v", url, result.Error())
				fmt.Fprintf(errLog, "Failed to get %s: %v\n", url, result.Error())
				continue
			}

			// This produces a byte array of json.
			b, err := result.Raw()

			if err == nil {
				obj, err := toObj(b, gv.String(), resource.Kind)
				if err != nil {
					return nil, err
				}
				objs[gv.String()+"/"+resource.Name] = obj
			}
		}
	}

	return objs, nil

}

// Get the cluster level resources
func (dc *DiscoveryClient) ResourcesForCluster(exclude ExcludeFilter, errLog io.Writer) (map[string]interface{}, error) {
	objs := make(map[string]interface{})

	lists, err := dc.discoveryClient.ServerPreferredResources()
	if err != nil {
		return nil, err
	}

	for _, list := range lists {
		if len(list.APIResources) == 0 {
			continue
		}
		for _, resource := range list.APIResources {
			logrus.Infof("[DEBUG]: COMMON prepare to checking specific resource: %s", resource.Name)
		}
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			continue
		}

		for _, resource := range list.APIResources {
			if resource.Namespaced {
				continue
			}

			if exclude(gv, resource) {
				continue
			}

			prefix := "apis"
			if gv.String() == "v1" {
				prefix = "api"
			}
			url := fmt.Sprintf("/%s/%s/%s", prefix, gv.String(), resource.Name)

			result := dc.discoveryClient.RESTClient().Get().AbsPath(url).Do(dc.Context)

			// It is likely that errors can occur.
			if result.Error() != nil {
				logrus.Tracef("Failed to get %s: %v", url, result.Error())
				fmt.Fprintf(errLog, "Failed to get %s: %v\n", url, result.Error())
				continue
			}

			b, err := result.Raw()

			if err == nil {
				obj, err := toObj(b, gv.String(), resource.Kind)
				if err != nil {
					return nil, err
				}
				objs[gv.String()+"/"+resource.Name] = obj
			}
		}
	}

	return objs, nil
}
