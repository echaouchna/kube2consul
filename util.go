package main

import (
	"strconv"
	"strings"

	"k8s.io/client-go/pkg/api/v1"
)

var (
	knownTags = []string{
		"name",
		"ignore",
		"check_http",
		"check_https",
		"check_interval",
		"check_timeout",
		"tag",
	}
)

func isKnowTag(a string) (bool, string) {
	for _, b := range knownTags {
		if strings.HasPrefix(a, b) {
			return true, b
		}
	}
	return false, ""
}

func mapDefault(m map[string]string, key, defaultValue string) string {
	v, ok := m[key]
	if !ok || v == "" {
		return defaultValue
	}
	return v
}

func addMetadata(metadata map[string]string, keyCaseSensitive, key, value string) string {
	if strings.HasPrefix(keyCaseSensitive, "TAG_") {
		tagKey := strings.TrimPrefix(keyCaseSensitive, "TAG_")
		if _, err := strconv.Atoi(tagKey); err == nil {
			metadata["tags"] = metadata["tags"] + value + ","
		} else {
			metadata["tags"] = metadata["tags"] + tagKey + "=" + value + ","
		}
	} else {
		metadata[key] = value
	}
	return key
}

func serviceMetaData(endpoint *v1.Endpoints, port string) (map[string]string, map[string]bool) {
	meta := make([]string, 0)
	for k, v := range endpoint.Labels {
		meta = append(meta, k+"="+v)
	}
	metadata := make(map[string]string)
	metadata["tags"] = ""
	metadataFromPort := make(map[string]bool)
	for _, kv := range meta {
		kvp := strings.SplitN(kv, "=", 2)
		if strings.HasPrefix(kvp[0], "SERVICE_") && len(kvp) > 1 {
			keyCaseSensitive := strings.TrimPrefix(kvp[0], "SERVICE_")
			key := strings.ToLower(keyCaseSensitive)
			if metadataFromPort[key] {
				continue
			}
			portkeyCaseSensitive := strings.SplitN(keyCaseSensitive, "_", 2)
			portkey := strings.SplitN(key, "_", 2)
			_, err := strconv.Atoi(portkey[0])
			if err == nil && len(portkey) > 1 {
				isKnown, _ := isKnowTag(portkey[1])
				if !isKnown || portkey[0] != port {
					continue
				}
				usedKey := addMetadata(metadata, portkeyCaseSensitive[1], portkey[1], kvp[1])
				metadataFromPort[usedKey] = true
			} else if isKnown, _ := isKnowTag(key); isKnown {
				addMetadata(metadata, keyCaseSensitive, key, kvp[1])
			}
		}
	}
	return metadata, metadataFromPort
}

func parseLabels(labels map[string]string) (tagsArray []string) {
	for key, value := range labels {
		tagsArray = append(tagsArray, key+"="+value)
	}
	return
}

func tagsToArray(tags string) []string {
	s := strings.Split(tags, ",")
	var r []string
	for _, str := range s {
		if str != "" {
			r = append(r, str)
		}
	}
	return r
}
