package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Praqma/helmsman/gcs"
	version "github.com/hashicorp/go-version"
)

var currentState map[string]releaseState

// releaseState represents the current state of a release
type releaseState struct {
	Revision        int
	Updated         time.Time
	Status          string
	Chart           string
	Namespace       string
	TillerNamespace string
}

type releaseInfo struct {
	Name            string `json:"Name"`
	Revision        int    `json:"Revision"`
	Updated         string `json:"Updated"`
	Status          string `json:"Status"`
	Chart           string `json:"Chart"`
	AppVersion      string `json:"AppVersion,omitempty"`
	Namespace       string `json:"Namespace"`
	TillerNamespace string `json:",omitempty"`
}

type tillerReleases struct {
	Next     string        `json:"Next"`
	Releases []releaseInfo `json:"Releases"`
}

// getHelmClientVersion returns Helm client Version
func getHelmClientVersion() string {
	cmd := command{
		Cmd:         "bash",
		Args:        []string{"-c", "helm version --client --short"},
		Description: "checking Helm version ",
	}

	exitCode, result := cmd.exec(debug, false)
	if exitCode != 0 {
		logError("ERROR: while checking helm version: " + result)
	}
	return result
}

// getAllReleases fetches a list of all releases in a k8s cluster
func getAllReleases() tillerReleases {

	// result := make(map[string]interface{})
	var result tillerReleases
	if _, ok := s.Namespaces["kube-system"]; !ok && !s.Settings.Tillerless {
		result.Releases = append(result.Releases, getTillerReleases("kube-system").Releases...)
	}

	for ns, v := range s.Namespaces {
		if (v.InstallTiller || v.UseTiller) || s.Settings.Tillerless {
			result.Releases = append(result.Releases, getTillerReleases(ns).Releases...)
		}
	}

	return result
}

// getTillerReleases gets releases deployed with a given Tiller (in a given namespace)
func getTillerReleases(tillerNS string) tillerReleases {
	v1, _ := version.NewVersion(helmVersion)
	jsonConstraint, _ := version.NewConstraint(">=2.10.0-rc.1")
	var outputFormat string
	if jsonConstraint.Check(v1) {
		outputFormat = "--output json"
	}

	output, err := helmList(tillerNS, outputFormat, "")
	if err != nil {
		logError(err.Error())
	}

	var allReleases tillerReleases
	if output == "" {
		return allReleases
	}

	if jsonConstraint.Check(v1) {
		allReleases, err = parseJSONListAndFollow(output, tillerNS)
		if err != nil {
			logError(err.Error())
		}
	} else {
		allReleases = parseTextList(output)
	}

	// appending tiller-namespace to each release found
	for i := 0; i < len(allReleases.Releases); i++ {
		allReleases.Releases[i].TillerNamespace = tillerNS
	}

	return allReleases
}

func parseJSONListAndFollow(input, tillerNS string) (tillerReleases, error) {
	var allReleases tillerReleases
	var releases tillerReleases

	for {
		output, err := helmList(tillerNS, "--output json", releases.Next)
		if err != nil {
			return allReleases, err
		}
		if err := json.Unmarshal([]byte(output), &releases); err != nil {
			return allReleases, fmt.Errorf("ERROR: failed to unmarshal Helm CLI output: %s", err)
		}
		for _, releaseInfo := range releases.Releases {
			allReleases.Releases = append(allReleases.Releases, releaseInfo)
		}
		if releases.Next == "" {
			break
		}
	}

	return allReleases, nil
}

func parseTextList(input string) tillerReleases {
	var out tillerReleases
	lines := strings.Split(input, "\n")
	for i, l := range lines {
		if l == "" || (strings.HasPrefix(strings.TrimSpace(l), "NAME") && strings.HasSuffix(strings.TrimSpace(l), "NAMESPACE")) {
			continue
		} else {
			r, _ := strconv.Atoi(strings.Fields(lines[i])[1])
			t := strings.Fields(lines[i])[2] + " " + strings.Fields(lines[i])[3] + " " + strings.Fields(lines[i])[4] + " " +
				strings.Fields(lines[i])[5] + " " + strings.Fields(lines[i])[6]
			out.Releases = append(out.Releases, releaseInfo{Name: strings.Fields(lines[i])[0], Revision: r, Updated: t, Status: strings.Fields(lines[i])[7], Chart: strings.Fields(lines[i])[8], Namespace: strings.Fields(lines[i])[9], AppVersion: "", TillerNamespace: ""})
		}
	}
	return out
}

func helmList(tillerNS, outputFormat, offset string) (string, error) {
	arg := fmt.Sprintf("%s list --all --max 0 --offset \"%s\" %s --tiller-namespace %s %s",
		helmCommand(tillerNS), offset, outputFormat, tillerNS, getNSTLSFlags(tillerNS),
	)
	cmd := command{
		Cmd:         "bash",
		Args:        []string{"-c", arg},
		Description: "listing all existing releases in namespace [ " + tillerNS + " ]...",
	}

	exitCode, result := cmd.exec(debug, verbose)
	if exitCode != 0 {
		if !apply {
			if strings.Contains(result, "incompatible versions") {
				return "", errors.New(result)
			}
			log.Println("INFO: " + strings.Replace(result, "Error: ", "", 1))
			return "", nil
		}

		return "", fmt.Errorf("ERROR: failed to list all releases in namespace [ %s ]: %s", tillerNS, result)
	}

	return result, nil
}

// buildState builds the currentState map containing information about all releases existing in a k8s cluster
func buildState() {
	log.Println("INFO: mapping the current helm state ...")

	currentState = make(map[string]releaseState)
	rel := getAllReleases()

	for i := 0; i < len(rel.Releases); i++ {
		// skipping the header from helm output
		time, err := time.Parse("Mon Jan _2 15:04:05 2006", rel.Releases[i].Updated)
		if err != nil {
			logError("ERROR: while converting release time: " + err.Error())
		}

		currentState[rel.Releases[i].Name+"-"+rel.Releases[i].TillerNamespace] = releaseState{
			Revision:        rel.Releases[i].Revision,
			Updated:         time,
			Status:          rel.Releases[i].Status,
			Chart:           rel.Releases[i].Chart,
			Namespace:       rel.Releases[i].Namespace,
			TillerNamespace: rel.Releases[i].TillerNamespace,
		}
	}
}

// helmRealseExists checks if a Helm release is/was deployed in a k8s cluster.
// It searches the Current State for releases.
// The key format for releases uniqueness is:  <release name - the Tiller namespace where it should be deployed >
// If status is provided as an input [deployed, deleted, failed], then the search will verify the release status matches the search status.
func helmReleaseExists(r *release, status string) (bool, releaseState) {
	compositeReleaseName := r.Name + "-" + getDesiredTillerNamespace(r)

	v, ok := currentState[compositeReleaseName]
	if !ok {
		return false, v
	}

	if status != "" {
		if v.Status == strings.ToUpper(status) {
			return true, v
		}
		return false, v
	}
	return true, v
}

// getReleaseRevision returns the revision number for a release
func getReleaseRevision(rs releaseState) string {

	return strconv.Itoa(rs.Revision)
}

// getReleaseChartName extracts and returns the Helm chart name from the chart info in a release state.
// example: chart in release state is "jenkins-0.9.0" and this function will extract "jenkins" from it.
func getReleaseChartName(rs releaseState) string {

	chart := rs.Chart
	runes := []rune(chart)
	return string(runes[0:strings.LastIndexByte(chart[0:strings.IndexByte(chart, '.')], '-')])
}

// getReleaseChartVersion extracts and returns the Helm chart version from the chart info in a release state.
// example: chart in release state is returns "jenkins-0.9.0" and this functions will extract "0.9.0" from it.
// It should also handle semver-valid pre-release/meta information, example: in: jenkins-0.9.0-1, out: 0.9.0-1
// in the event of an error, an empty string is returned.
func getReleaseChartVersion(rs releaseState) string {
	chart := rs.Chart
	re := regexp.MustCompile("-(v?[0-9]+\\.[0-9]+\\.[0-9]+.*)")
	matches := re.FindStringSubmatch(chart)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

// getNSTLSFlags returns TLS flags for a given namespace if it's deployed with TLS
func getNSTLSFlags(namespace string) string {
	tls := ""
	ns := s.Namespaces[namespace]
	if tillerTLSEnabled(ns) {

		tls = " --tls --tls-ca-cert " + namespace + "-ca.cert --tls-cert " + namespace + "-client.cert --tls-key " + namespace + "-client.key "
	}
	return tls
}

// validateReleaseCharts validates if the charts defined in a release are valid.
// Valid charts are the ones that can be found in the defined repos.
// This function uses Helm search to verify if the chart can be found or not.
func validateReleaseCharts(apps map[string]*release) (bool, string) {
	versionExtractor := regexp.MustCompile(`version:\s?(.*)`)

	for app, r := range apps {
		validateCurrentChart := true
		if len(targetMap) > 0 {
			if _, ok := targetMap[r.Name]; !ok {
				validateCurrentChart = false
			}
		}
		if validateCurrentChart {
			if isLocalChart(r.Chart) {
				cmd := command{
					Cmd:         "bash",
					Args:        []string{"-c", "helm inspect chart '" + r.Chart + "'"},
					Description: "validating if chart at " + r.Chart + " is available.",
				}

				var output string
				var exitCode int
				if exitCode, output = cmd.exec(debug, verbose); exitCode != 0 {
					maybeRepo := filepath.Base(filepath.Dir(r.Chart))
					return false, "ERROR: chart at " + r.Chart + " for app [" + app + "] could not be found. Did you mean to add a repo named '" + maybeRepo + "'?"
				}
				matches := versionExtractor.FindStringSubmatch(output)
				if len(matches) == 2 {
					version := matches[1]
					if r.Version != version {
						return false, "ERROR: chart " + r.Chart + " with version " + r.Version + " is specified for " +
							"app [" + app + "] but the chart found at that path has version " + version + " which does not match."
					}
				}

			} else {
				cmd := command{
					Cmd:         "bash",
					Args:        []string{"-c", "helm search " + r.Chart + " --version " + strconv.Quote(r.Version) + " -l"},
					Description: "validating if chart " + r.Chart + "-" + r.Version + " is available in the defined repos.",
				}

				if exitCode, result := cmd.exec(debug, verbose); exitCode != 0 || strings.Contains(result, "No results found") {
					return false, "ERROR: chart " + r.Chart + "-" + r.Version + " is specified for " +
						"app [" + app + "] but is not found in the defined repos."
				}
			}
		}

	}
	return true, ""
}

// getChartVersion fetches the lastest chart version matching the semantic versioning constraints.
// If chart is local, returns the given release version
func getChartVersion(r *release) (string, string) {
	if isLocalChart(r.Chart) {
		return r.Version, ""
	}
	cmd := command{
		Cmd:         "bash",
		Args:        []string{"-c", "helm search " + r.Chart + " --version " + strconv.Quote(r.Version)},
		Description: "getting latest chart version " + r.Chart + "-" + r.Version + "",
	}

	if exitCode, result := cmd.exec(debug, verbose); exitCode != 0 || strings.Contains(result, "No results found") {
		return "", "ERROR: chart " + r.Chart + "-" + r.Version + " is specified for " + "but version is not found in the defined repo."
	} else {
		versions := strings.Split(result, "\n")
		if len(versions) < 2 {
			return "", "ERROR: chart " + r.Chart + "-" + r.Version + " is specified for " + "but version is not found in the defined repo."
		}
		fields := strings.Split(versions[1], "\t")
		if len(fields) != 4 {
			return "", "ERROR: chart " + r.Chart + "-" + r.Version + " is specified for " + "but version is not found in the defined repo."
		}
		return strings.TrimSpace(fields[1]), ""
	}
}

// waitForTiller keeps checking if the helm Tiller is ready or not by executing helm list and checking its error (if any)
// waits for 5 seconds before each new attempt and eventually terminates after 10 failed attempts.
func waitForTiller(namespace string) {

	attempt := 0

	cmd := command{
		Cmd:         "bash",
		Args:        []string{"-c", "helm list --tiller-namespace " + namespace + getNSTLSFlags(namespace)},
		Description: "checking if helm Tiller is ready in namespace [ " + namespace + " ].",
	}

	exitCode, err := cmd.exec(debug, verbose)

	for attempt < 10 {
		if exitCode == 0 {
			return
		} else if strings.Contains(err, "could not find a ready tiller pod") || strings.Contains(err, "could not find tiller") {
			log.Println("INFO: waiting for helm Tiller to be ready in namespace [" + namespace + "] ...")
			time.Sleep(5 * time.Second)
			exitCode, err = cmd.exec(debug, verbose)
		} else {
			logError("ERROR: while waiting for helm Tiller to be ready in namespace [ " + namespace + " ] : " + err)
		}
		attempt = attempt + 1
	}
	logError("ERROR: timeout reached while waiting for helm Tiller to be ready in namespace [ " + namespace + " ]. Aborting!")
}

// addHelmRepos adds repositories to Helm if they don't exist already.
// Helm does not mind if a repo with the same name exists. It treats it as an update.
func addHelmRepos(repos map[string]string) (bool, string) {

	for repoName, repoLink := range repos {
		basicAuth := ""
		// check if repo is in GCS, then perform GCS auth -- needed for private GCS helm repos
		// failed auth would not throw an error here, as it is possible that the repo is public and does not need authentication
		if strings.HasPrefix(repoLink, "gs://") {
			gcs.Auth()
		}

		u, err := url.Parse(repoLink)
		if err != nil {
			logError("ERROR: failed to add helm repo:  " + err.Error())
		}
		if u.User != nil {
			p, ok := u.User.Password()
			if !ok {
				logError("ERROR: helm repo " + repoName + " has incomplete basic auth info. Missing the password!")
			}
			basicAuth = " --username " + u.User.Username() + " --password " + p

		}

		cmd := command{
			Cmd:         "bash",
			Args:        []string{"-c", "helm repo add " + basicAuth + " " + repoName + " " + strconv.Quote(repoLink)},
			Description: "adding repo " + repoName,
		}

		if exitCode, err := cmd.exec(debug, verbose); exitCode != 0 {
			return false, "ERROR: while adding repo [" + repoName + "]: " + err
		}

	}

	cmd := command{
		Cmd:         "bash",
		Args:        []string{"-c", "helm repo update "},
		Description: "updating helm repos",
	}

	if exitCode, err := cmd.exec(debug, verbose); exitCode != 0 {
		return false, "ERROR: while updating helm repos : " + err
	}

	return true, ""
}

// deployTiller deploys Helm's Tiller in a specific namespace with a serviceAccount
// If serviceAccount is not provided (empty string), the defaultServiceAccount is used.
// If no defaultServiceAccount is provided, A service account is created and Tiller is deployed with the new service account
// If no namespace is provided, Tiller is deployed to kube-system
func deployTiller(namespace string, serviceAccount string, defaultServiceAccount string, role string, roleTemplateFile string, tillerMaxHistory int) (bool, string) {
	log.Println("INFO: deploying Tiller in namespace [ " + namespace + " ].")
	sa := ""
	if serviceAccount != "" {
		if ok, err := validateServiceAccount(serviceAccount, namespace); !ok {
			if strings.Contains(err, "NotFound") || strings.Contains(err, "not found") {

				log.Println("INFO: service account [ " + serviceAccount + " ] does not exist in namespace [ " + namespace + " ] .. attempting to create it ... ")
				if _, rbacErr := createRBAC(serviceAccount, namespace, role, roleTemplateFile); rbacErr != "" {
					return false, rbacErr
				}
			} else {
				return false, "ERROR: while validating/creating service account [ " + serviceAccount + " ] in namespace [" + namespace + "]: " + err
			}
		}
		sa = " --service-account " + serviceAccount
	} else {
		roleName := "helmsman-tiller"
		defaultServiceAccountName := "helmsman"

		if defaultServiceAccount != "" {
			defaultServiceAccountName = defaultServiceAccount
		}
		if role != "" {
			roleName = role
		}

		if ok, err := validateServiceAccount(defaultServiceAccountName, namespace); !ok {
			if strings.Contains(err, "NotFound") || strings.Contains(err, "not found") {

				log.Println("INFO: service account [ " + defaultServiceAccountName + " ] does not exist in namespace [ " + namespace + " ] .. attempting to create it ... ")
				if _, rbacErr := createRBAC(defaultServiceAccountName, namespace, roleName, roleTemplateFile); rbacErr != "" {
					return false, rbacErr
				}
			} else {
				return false, "ERROR: while validating/creating service account [ " + defaultServiceAccountName + " ] in namespace [" + namespace + "]: " + err
			}
		}
		sa = " --service-account " + defaultServiceAccountName
	}
	if namespace == "" {
		namespace = "kube-system"
	}
	tillerNameSpace := " --tiller-namespace " + namespace

	maxHistory := ""
	if tillerMaxHistory > 0 {
		maxHistory = " --history-max " + strconv.Itoa(tillerMaxHistory)
	}
	tls := ""
	ns := s.Namespaces[namespace]
	if tillerTLSEnabled(ns) {
		tillerCert := namespace + "-tiller.cert"
		tillerKey := namespace + "-tiller.key"
		caCert := namespace + "-ca.cert"

		tls = " --tiller-tls --tiller-tls-cert " + tillerCert + " --tiller-tls-key " + tillerKey + " --tiller-tls-verify --tls-ca-cert " + caCert
	}

	storageBackend := ""
	if s.Settings.StorageBackend == "secret" {
		storageBackend = " --override 'spec.template.spec.containers[0].command'='{/tiller,--storage=secret}'"
	}
	cmd := command{
		Cmd:         "bash",
		Args:        []string{"-c", "helm init --force-upgrade " + maxHistory + sa + tillerNameSpace + tls + storageBackend},
		Description: "initializing helm on the current context and upgrading Tiller on namespace [ " + namespace + " ].",
	}

	if exitCode, err := cmd.exec(debug, verbose); exitCode != 0 {
		return false, "ERROR: while deploying Helm Tiller in namespace [" + namespace + "]: " + err
	}
	return true, ""
}

// initHelmClientOnly initializes the helm client only (without deploying Tiller)
func initHelmClientOnly() (bool, string) {
	cmd := command{
		Cmd:         "bash",
		Args:        []string{"-c", "helm init --client-only "},
		Description: "initializing helm on the client only.",
	}

	if exitCode, err := cmd.exec(debug, verbose); exitCode != 0 {
		return false, "ERROR: initializing helm on the client : " + err
	}

	return true, ""
}

// initHelm initializes helm on a k8s cluster and deploys Tiller in one or more namespaces
func initHelm() (bool, string) {
	defaultSA := s.Settings.ServiceAccount
	if !s.Settings.Tillerless {
		for k, ns := range s.Namespaces {
			if tillerTLSEnabled(ns) {
				downloadFile(s.Namespaces[k].TillerCert, k+"-tiller.cert")
				downloadFile(s.Namespaces[k].TillerKey, k+"-tiller.key")
				downloadFile(s.Namespaces[k].CaCert, k+"-ca.cert")
				// client cert and key
				downloadFile(s.Namespaces[k].ClientCert, k+"-client.cert")
				downloadFile(s.Namespaces[k].ClientKey, k+"-client.key")
			}
			if ns.InstallTiller && k != "kube-system" {
				if ok, err := deployTiller(k, ns.TillerServiceAccount, defaultSA, ns.TillerRole, ns.TillerRoleTemplateFile, ns.TillerMaxHistory); !ok {
					return false, err
				}
			}
		}

		if ns, ok := s.Namespaces["kube-system"]; ok {
			if ns.InstallTiller {
				if ok, err := deployTiller("kube-system", ns.TillerServiceAccount, defaultSA, ns.TillerRole, ns.TillerRoleTemplateFile, ns.TillerMaxHistory); !ok {
					return false, err
				}
			}
		} else {
			if ok, err := deployTiller("kube-system", "", defaultSA, ns.TillerRole, ns.TillerRoleTemplateFile, ns.TillerMaxHistory); !ok {
				return false, err
			}
		}
	} else {
		log.Println("INFO: skipping Tiller deployments because Tillerless mode is enabled.")
	}

	return true, ""
}

// cleanUntrackedReleases checks for any releases that are managed by Helmsman and are no longer tracked by the desired state
// It compares the currently deployed releases with "MANAGED-BY=HELMSMAN" labels with Apps defined in the desired state
// For all untracked releases found, a decision is made to delete them and is added to the Helmsman plan
// NOTE: Untracked releases don't benefit from either namespace or application protection.
// NOTE: Removing/Commenting out an app from the desired state makes it untracked.
func cleanUntrackedReleases() {
	toDelete := make(map[string]map[string]bool)
	log.Println("INFO: checking if any Helmsman managed releases are no longer tracked by your desired state ...")
	for ns, releases := range getHelmsmanReleases() {
		for r := range releases {
			tracked := false
			for _, app := range s.Apps {
				if app.Name == r && getDesiredTillerNamespace(app) == ns {
					tracked = true
				}
			}
			if !tracked {
				if _, ok := toDelete[ns]; !ok {
					toDelete[ns] = make(map[string]bool)
				}
				toDelete[ns][r] = true
			}
		}
	}

	if len(toDelete) == 0 {
		log.Println("INFO: no untracked releases found.")
	} else {
		for ns, releases := range toDelete {
			for r := range releases {
				if len(targetMap) > 0 {
					if _, ok := targetMap[r]; !ok {
						logDecision("DECISION: untracked release [ "+r+" ] is ignored by target flag. Skipping.", -800, noop)
					} else {
						logDecision("DECISION: untracked release found: release [ "+r+" ] from Tiller in namespace [ "+ns+" ]. It will be deleted.", -800, delete)
						deleteUntrackedRelease(r, ns)
					}
				}
			}
		}
	}
}

// deleteUntrackedRelease creates the helm command to purge delete an untracked release
func deleteUntrackedRelease(release string, tillerNamespace string) {

	tls := ""
	ns := s.Namespaces[tillerNamespace]
	if tillerTLSEnabled(ns) {

		tls = " --tls --tls-ca-cert " + tillerNamespace + "-ca.cert --tls-cert " + tillerNamespace + "-client.cert --tls-key " + tillerNamespace + "-client.key "
	}
	cmd := command{
		Cmd:         "bash",
		Args:        []string{"-c", helmCommand(tillerNamespace) + " delete --purge " + release + " --tiller-namespace " + tillerNamespace + tls + getDryRunFlags()},
		Description: "deleting untracked release [ " + release + " ] from Tiller in namespace [[ " + tillerNamespace + " ]]",
	}

	outcome.addCommand(cmd, -800, nil)
}

// decrypt a helm secret file
func decryptSecret(name string) bool {
	cmd := command{
		Cmd:         "bash",
		Args:        []string{"-c", "helm secrets dec " + name},
		Description: "Decrypting " + name,
	}

	exitCode, _ := cmd.exec(debug, false)

	if exitCode != 0 {
		return false
	}

	return true
}

// updateChartDep updates dependencies for a local chart
func updateChartDep(chartPath string) (bool, string) {
	cmd := command{
		Cmd:         "bash",
		Args:        []string{"-c", "helm dependency update " + chartPath},
		Description: "Updateing dependency for local chart " + chartPath,
	}

	exitCode, err := cmd.exec(debug, verbose)

	if exitCode != 0 {
		return false, err
	}
	return true, ""
}
