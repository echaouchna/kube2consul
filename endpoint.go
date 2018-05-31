package main

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/client-go/pkg/api/v1"
)

// Endpoint is a summary of kubernetes endpoint
type Endpoint struct {
	Name    string
	Address string
	Port    int32
	RefName string
	Tags    []string
}

var (
	consulPrefix      = "consul_"
	serviceNameSuffix = "NAME"
	servicePrefix     = "SERVICE"
	ignoreSuffix      = "IGNORE"
	separator         = "_"
)

// NewEndpoint allows to create Endpoint
func NewEndpoint(name, address string, port int32, refName string, tags []string) Endpoint {
	return Endpoint{name, address, port, refName, tags}
}

func parseConsulLabels(labels map[string]string) (tagsArray []string) {
	for key, value := range labels {
		if strings.HasPrefix(key, consulPrefix) && !strings.HasPrefix(key, consulPrefix+servicePrefix) {
			tagKey := strings.Replace(key, consulPrefix, "", -1)
			tagsArray = append(tagsArray, tagKey+"="+value)
		}
	}
	return
}

func (k2c *kube2consul) generateEntries(endpoint *v1.Endpoints) ([]Endpoint, map[string][]Endpoint) {
	var (
		eps                 []Endpoint
		refName             string
		registerService     = false
		perServiceEndpoints = make(map[string][]Endpoint)
	)

	for key := range endpoint.Labels {
		if strings.HasPrefix(key, consulPrefix) && strings.HasSuffix(key, serviceNameSuffix) {
			registerService = true
			break
		}
	}

	regitratorLabelPrefix := consulPrefix + servicePrefix + separator
	ignoreEntireService := false
	if _, ok := endpoint.Labels[regitratorLabelPrefix+ignoreSuffix]; ok {
		ignoreEntireService = true
	}

	if registerService && !ignoreEntireService {
		for _, subset := range endpoint.Subsets {
			appendPort := false
			if len(subset.Ports) > 1 {
				appendPort = true
			}
			for _, port := range subset.Ports {
				servicePort := strconv.Itoa((int)(port.Port))
				serviceName := endpoint.Name
				if _, ok := endpoint.Labels[regitratorLabelPrefix+servicePort+separator+ignoreSuffix]; ok {
					continue
				}
				if nameLabelValue, ok := endpoint.Labels[regitratorLabelPrefix+serviceNameSuffix]; ok {
					serviceName = nameLabelValue
				}
				if appendPort {
					serviceName = serviceName + "_" + servicePort
				}
				if nameLabelValue, ok := endpoint.Labels[regitratorLabelPrefix+servicePort+separator+serviceNameSuffix]; ok {
					serviceName = nameLabelValue
				}
				for _, addr := range subset.Addresses {
					if addr.TargetRef != nil {
						refName = addr.TargetRef.Name
					}
					newEndpoint := NewEndpoint(serviceName, addr.IP, port.Port, refName, parseConsulLabels(endpoint.Labels))
					eps = append(eps, newEndpoint)
					perServiceEndpoints[serviceName] = append(perServiceEndpoints[serviceName], newEndpoint)
				}
			}
		}
	}

	return eps, perServiceEndpoints
}

func (k2c *kube2consul) updateEndpoints(ep *v1.Endpoints) error {
	endpoints, perServiceEndpoints := k2c.generateEntries(ep)
	for _, e := range endpoints {
		if err := k2c.registerEndpoint(e); err != nil {
			return fmt.Errorf("Error updating endpoints %v: %v", ep.Name, err)
		}
	}

	for serviceName, e := range perServiceEndpoints {
		if err := k2c.removeDeletedEndpoints(serviceName, e); err != nil {
			return fmt.Errorf("Error removing possible deleted endpoints: %v: %v", serviceName, err)
		}
	}
	return nil
}
