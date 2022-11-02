package manager

/* return map(<resource>: [namespace]) */
func getHarvesterExtrResource() map[string][]string {
	extraResource := make(map[string][]string)

	extraResource["secrets"] = []string{"fleet-local"}
	return extraResource
}
