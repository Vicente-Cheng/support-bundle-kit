package manager

/* return map(<namespace>: [resource]) */
func getHarvesterExtrResource() map[string][]string {
	extraResources := make(map[string][]string)

	//extraResource["secrets"] = []string{"fleet-local"}
	extraResources["fleet-local"] = []string{"secrets"}
	return extraResources
}
