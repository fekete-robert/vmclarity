// Copyright © 2023 Cisco Systems, Inc. and its affiliates.
// All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"
	"fmt"
	"github.com/openclarity/vmclarity/shared/pkg/families/malware"

	cdx "github.com/CycloneDX/cyclonedx-go"
	"github.com/openclarity/kubeclarity/shared/pkg/scanner"
	"github.com/openclarity/kubeclarity/shared/pkg/utils/cyclonedx_helper"

	"github.com/openclarity/vmclarity/api/models"
	"github.com/openclarity/vmclarity/shared/pkg/backendclient"
	"github.com/openclarity/vmclarity/shared/pkg/families"
	"github.com/openclarity/vmclarity/shared/pkg/families/exploits"
	"github.com/openclarity/vmclarity/shared/pkg/families/results"
	"github.com/openclarity/vmclarity/shared/pkg/families/sbom"
	"github.com/openclarity/vmclarity/shared/pkg/families/secrets"

	"github.com/openclarity/vmclarity/shared/pkg/families/types"
	"github.com/openclarity/vmclarity/shared/pkg/families/vulnerabilities"
	"github.com/openclarity/vmclarity/shared/pkg/utils"
)

type Exporter struct {
	client *backendclient.BackendClient
}

func CreateExporter() (*Exporter, error) {
	client, err := backendclient.Create(server)
	if err != nil {
		return nil, fmt.Errorf("unable to create VMClarity API client. server=%v: %w", server, err)
	}

	return &Exporter{
		client: client,
	}, nil
}

func convertSBOMResultToAPIModel(sbomResults *sbom.Results) *models.SbomScan {
	packages := []models.Package{}

	if sbomResults.SBOM.Components != nil {
		for _, component := range *sbomResults.SBOM.Components {
			packages = append(packages, *convertPackageInfoToAPIModel(component))
		}
	}

	return &models.SbomScan{
		Packages: &packages,
	}
}

func convertPackageInfoToAPIModel(component cdx.Component) *models.Package {
	return &models.Package{
		Cpes:     utils.PointerTo([]string{component.CPE}),
		Language: utils.PointerTo(cyclonedx_helper.GetComponentLanguage(component)),
		Licenses: convertPackageLicencesToAPIModel(component.Licenses),
		Name:     utils.PointerTo(component.Name),
		Purl:     utils.PointerTo(component.PackageURL),
		Type:     utils.PointerTo(string(component.Type)),
		Version:  utils.PointerTo(component.Version),
	}
}

func convertPackageLicencesToAPIModel(licenses *cdx.Licenses) *[]string {
	if licenses == nil {
		return nil
	}
	// nolint:prealloc
	var ret []string
	for _, lic := range *licenses {
		if lic.License == nil {
			continue
		}
		ret = append(ret, lic.License.Name)
	}

	return &ret
}

func convertVulnResultToAPIModel(vulnerabilitiesResults *vulnerabilities.Results) *models.VulnerabilityScan {
	// nolint:prealloc
	var vuls []models.Vulnerability
	for _, vulCandidates := range vulnerabilitiesResults.MergedResults.MergedVulnerabilitiesByKey {
		if len(vulCandidates) < 1 {
			continue
		}

		vulCandidate := vulCandidates[0]

		vul := models.Vulnerability{
			Cvss:              convertVulnCvssToAPIModel(vulCandidate.Vulnerability.CVSS),
			Description:       utils.PointerTo(vulCandidate.Vulnerability.Description),
			Distro:            convertVulnDistroToAPIModel(vulCandidate.Vulnerability.Distro),
			Fix:               convertVulnFixToAPIModel(vulCandidate.Vulnerability.Fix),
			LayerId:           utils.PointerTo(vulCandidate.Vulnerability.LayerID),
			Links:             utils.PointerTo(vulCandidate.Vulnerability.Links),
			Package:           convertVulnPackageToAPIModel(vulCandidate.Vulnerability.Package),
			Path:              utils.PointerTo(vulCandidate.Vulnerability.Path),
			Severity:          utils.PointerTo(models.VulnerabilitySeverity(vulCandidate.Vulnerability.Severity)),
			VulnerabilityName: utils.PointerTo(vulCandidate.Vulnerability.ID),
		}
		vuls = append(vuls, vul)
	}

	return &models.VulnerabilityScan{
		Vulnerabilities: &vuls,
	}
}

func convertVulnFixToAPIModel(fix scanner.Fix) *models.VulnerabilityFix {
	return &models.VulnerabilityFix{
		State:    utils.PointerTo(fix.State),
		Versions: utils.PointerTo(fix.Versions),
	}
}

func convertVulnDistroToAPIModel(distro scanner.Distro) *models.VulnerabilityDistro {
	return &models.VulnerabilityDistro{
		IDLike:  utils.PointerTo(distro.IDLike),
		Name:    utils.PointerTo(distro.Name),
		Version: utils.PointerTo(distro.Version),
	}
}

func convertVulnPackageToAPIModel(p scanner.Package) *models.Package {
	return &models.Package{
		Cpes:     utils.PointerTo(p.CPEs),
		Language: utils.PointerTo(p.Language),
		Licenses: utils.PointerTo(p.Licenses),
		Name:     utils.PointerTo(p.Name),
		Purl:     utils.PointerTo(p.PURL),
		Type:     utils.PointerTo(p.Type),
		Version:  utils.PointerTo(p.Version),
	}
}

func convertVulnCvssToAPIModel(cvss []scanner.CVSS) *[]models.VulnerabilityCvss {
	if cvss == nil {
		return nil
	}
	// nolint:prealloc
	var ret []models.VulnerabilityCvss
	for _, c := range cvss {
		var exploitabilityScore *float32
		if c.Metrics.ExploitabilityScore != nil {
			exploitabilityScore = utils.PointerTo[float32](float32(*c.Metrics.ExploitabilityScore))
		}
		var impactScore *float32
		if c.Metrics.ImpactScore != nil {
			impactScore = utils.PointerTo[float32](float32(*c.Metrics.ImpactScore))
		}
		ret = append(ret, models.VulnerabilityCvss{
			Metrics: &models.VulnerabilityCvssMetrics{
				BaseScore:           utils.PointerTo(float32(c.Metrics.BaseScore)),
				ExploitabilityScore: exploitabilityScore,
				ImpactScore:         impactScore,
			},
			Vector:  utils.PointerTo(c.Vector),
			Version: utils.PointerTo(c.Version),
		})
	}

	return &ret
}

func (e *Exporter) MarkScanResultInProgress() error {
	scanResult, err := e.client.GetScanResult(context.TODO(), scanResultID, models.GetScanResultsScanResultIDParams{})
	if err != nil {
		return fmt.Errorf("failed to get scan result: %w", err)
	}

	if scanResult.Status == nil {
		scanResult.Status = &models.TargetScanStatus{}
	}
	if scanResult.Status.General == nil {
		scanResult.Status.General = &models.TargetScanState{}
	}

	state := models.INPROGRESS
	scanResult.Status.General.State = &state

	err = e.client.PatchScanResult(context.TODO(), scanResult, scanResultID)
	if err != nil {
		return fmt.Errorf("failed to patch scan result: %w", err)
	}

	return nil
}

func (e *Exporter) MarkScanResultDone(errors []error) error {
	scanResult, err := e.client.GetScanResult(context.TODO(), scanResultID, models.GetScanResultsScanResultIDParams{})
	if err != nil {
		return fmt.Errorf("failed to get scan result: %w", err)
	}

	if scanResult.Status == nil {
		scanResult.Status = &models.TargetScanStatus{}
	}
	if scanResult.Status.General == nil {
		scanResult.Status.General = &models.TargetScanState{}
	}

	state := models.DONE
	scanResult.Status.General.State = &state

	// If we had any errors running the family or exporting results add it
	// to the general errors
	if len(errors) > 0 {
		var errorStrs []string
		// Pull the errors list out so that we can append to it (if there are
		// any errors at this point I would have hoped the orcestrator wouldn't
		// have spawned the VM) but we never know.
		if scanResult.Status.General.Errors != nil {
			errorStrs = *scanResult.Status.General.Errors
		}
		for _, err := range errors {
			if err != nil {
				errorStrs = append(errorStrs, err.Error())
			}
		}
		if len(errorStrs) > 0 {
			scanResult.Status.General.Errors = &errorStrs
		}
	}

	err = e.client.PatchScanResult(context.TODO(), scanResult, scanResultID)
	if err != nil {
		return fmt.Errorf("failed to patch scan result: %w", err)
	}

	return nil
}

func (e *Exporter) ExportSbomResult(res *results.Results, famerr families.RunErrors) error {
	scanResult, err := e.client.GetScanResult(context.TODO(), scanResultID, models.GetScanResultsScanResultIDParams{})
	if err != nil {
		return fmt.Errorf("failed to get scan result: %w", err)
	}

	if scanResult.Status == nil {
		scanResult.Status = &models.TargetScanStatus{}
	}
	if scanResult.Status.Sbom == nil {
		scanResult.Status.Sbom = &models.TargetScanState{}
	}
	if scanResult.Summary == nil {
		scanResult.Summary = &models.ScanFindingsSummary{}
	}

	errors := []string{}

	if err, ok := famerr[types.SBOM]; ok {
		errors = append(errors, err.Error())
	} else {
		sbomResults, err := results.GetResult[*sbom.Results](res)
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to get sbom from scan: %w", err).Error())
		} else {
			scanResult.Sboms = convertSBOMResultToAPIModel(sbomResults)
			if scanResult.Sboms.Packages != nil {
				scanResult.Summary.TotalPackages = utils.PointerTo(len(*scanResult.Sboms.Packages))
			}
		}
	}

	state := models.DONE
	scanResult.Status.Sbom.State = &state
	scanResult.Status.Sbom.Errors = &errors

	err = e.client.PatchScanResult(context.TODO(), scanResult, scanResultID)
	if err != nil {
		return fmt.Errorf("failed to patch scan result: %w", err)
	}

	return nil
}

func (e *Exporter) ExportVulResult(res *results.Results, famerr families.RunErrors) error {
	scanResult, err := e.client.GetScanResult(context.TODO(), scanResultID, models.GetScanResultsScanResultIDParams{})
	if err != nil {
		return fmt.Errorf("failed to get scan result: %w", err)
	}

	if scanResult.Status == nil {
		scanResult.Status = &models.TargetScanStatus{}
	}
	if scanResult.Status.Vulnerabilities == nil {
		scanResult.Status.Vulnerabilities = &models.TargetScanState{}
	}
	if scanResult.Summary == nil {
		scanResult.Summary = &models.ScanFindingsSummary{}
	}

	errors := []string{}

	if err, ok := famerr[types.Vulnerabilities]; ok {
		errors = append(errors, err.Error())
	} else {
		vulnerabilitiesResults, err := results.GetResult[*vulnerabilities.Results](res)
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to get vulnerabilities from scan: %w", err).Error())
		} else {
			scanResult.Vulnerabilities = convertVulnResultToAPIModel(vulnerabilitiesResults)
		}
		scanResult.Summary.TotalVulnerabilities = utils.GetVulnerabilityTotalsPerSeverity(scanResult.Vulnerabilities.Vulnerabilities)
	}

	state := models.DONE
	scanResult.Status.Vulnerabilities.State = &state
	scanResult.Status.Vulnerabilities.Errors = &errors

	err = e.client.PatchScanResult(context.TODO(), scanResult, scanResultID)
	if err != nil {
		return fmt.Errorf("failed to patch scan result: %w", err)
	}

	return nil
}

func (e *Exporter) ExportSecretsResult(res *results.Results, famerr families.RunErrors) error {
	scanResult, err := e.client.GetScanResult(context.TODO(), scanResultID, models.GetScanResultsScanResultIDParams{})
	if err != nil {
		return fmt.Errorf("failed to get scan result: %w", err)
	}

	if scanResult.Status == nil {
		scanResult.Status = &models.TargetScanStatus{}
	}
	if scanResult.Status.Secrets == nil {
		scanResult.Status.Secrets = &models.TargetScanState{}
	}

	if scanResult.Summary == nil {
		scanResult.Summary = &models.ScanFindingsSummary{}
	}

	errors := []string{}

	if err, ok := famerr[types.Secrets]; ok {
		errors = append(errors, err.Error())
	} else {
		secretsResults, err := results.GetResult[*secrets.Results](res)
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to get secrets results from scan: %w", err).Error())
		} else {
			scanResult.Secrets = convertSecretsResultToAPIModel(secretsResults)
			if scanResult.Secrets.Secrets != nil {
				scanResult.Summary.TotalSecrets = utils.PointerTo(len(*scanResult.Secrets.Secrets))
			}
		}
	}

	state := models.DONE
	scanResult.Status.Secrets.State = &state
	scanResult.Status.Secrets.Errors = &errors

	err = e.client.PatchScanResult(context.TODO(), scanResult, scanResultID)
	if err != nil {
		return fmt.Errorf("failed to patch scan result: %w", err)
	}

	return nil
}

func (e *Exporter) ExportMalwareResult(res *results.Results, famerr families.RunErrors) error {
	scanResult, err := e.client.GetScanResult(context.TODO(), scanResultID, models.GetScanResultsScanResultIDParams{})
	if err != nil {
		return fmt.Errorf("failed to get scan result: %w", err)
	}

	if scanResult.Status == nil {
		scanResult.Status = &models.TargetScanStatus{}
	}
	if scanResult.Status.Malware == nil {
		scanResult.Status.Malware = &models.TargetScanState{}
	}

	errors := []string{}

	if err, ok := famerr[types.Malware]; ok {
		errors = append(errors, err.Error())
	} else {
		malwareResults, err := results.GetResult[*malware.Results](res)
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to get malware results from scan: %w", err).Error())
		} else {
			scanResult.Malware = convertMalwareResultToAPIModel(malwareResults)
			if scanResult.Malware.Malware != nil {
				scanResult.Summary.TotalMalware = utils.PointerTo[int](len(*scanResult.Malware.Malware))
			}
		}
	}

	state := models.DONE
	scanResult.Status.Malware.State = &state
	scanResult.Status.Malware.Errors = &errors

	if err = e.client.PatchScanResult(context.TODO(), scanResult, scanResultID); err != nil {
		return fmt.Errorf("failed to patch scan result: %w", err)
	}

	return nil
}

func convertMalwareResultToAPIModel(malwareResults *malware.Results) *models.MalwareScan {
	if malwareResults == nil || malwareResults.MergedResults == nil {
		return &models.MalwareScan{}
	}

	malwareList := []models.Malware{}
	for _, m := range malwareResults.MergedResults.DetectedMalware {
		mal := m // Prevent loop variable pointer export
		malwareList = append(malwareList, models.Malware{
			MalwareName: &mal.MalwareName,
			MalwareType: &mal.MalwareType,
			Path:        &mal.Path,
		})
	}

	metadata := []models.ScannerMetadata{}
	for name, summary := range malwareResults.MergedResults.Metadata {
		nameVal := name // Prevent loop variable pointer export
		metadata = append(metadata, models.ScannerMetadata{
			ScannerName: &nameVal,
			ScannerSummary: &models.ScannerSummary{
				DataRead:           &summary.DataRead,
				DataScanned:        &summary.DataScanned,
				EngineVersion:      &summary.EngineVersion,
				InfectedFiles:      &summary.InfectedFiles,
				KnownViruses:       &summary.KnownViruses,
				ScannedDirectories: &summary.ScannedDirectories,
				ScannedFiles:       &summary.ScannedFiles,
				SuspectedFiles:     &summary.SuspectedFiles,
				TimeTaken:          &summary.TimeTaken,
			},
		})
	}

	return &models.MalwareScan{
		Malware:  &malwareList,
		Metadata: &metadata,
	}
}

func convertSecretsResultToAPIModel(secretsResults *secrets.Results) *models.SecretScan {
	if secretsResults == nil || secretsResults.MergedResults == nil {
		return &models.SecretScan{}
	}

	var secretsSlice []models.Secret
	for _, resultsCandidate := range secretsResults.MergedResults.Results {
		for i := range resultsCandidate.Findings {
			finding := resultsCandidate.Findings[i]
			secretsSlice = append(secretsSlice, models.Secret{
				Description: &finding.Description,
				EndLine:     &finding.EndLine,
				FilePath:    &finding.File,
				Fingerprint: &finding.Fingerprint,
				StartLine:   &finding.StartLine,
			})
		}
	}

	if secretsSlice == nil {
		return &models.SecretScan{}
	}

	return &models.SecretScan{
		Secrets: &secretsSlice,
	}
}

func convertExploitsResultToAPIModel(exploitsResults *exploits.Results) *models.ExploitScan {
	if exploitsResults == nil || exploitsResults.Exploits == nil {
		return &models.ExploitScan{}
	}

	// nolint:prealloc
	var retExploits []models.Exploit

	for i := range exploitsResults.Exploits {
		exploit := exploitsResults.Exploits[i]
		retExploits = append(retExploits, models.Exploit{
			CveID:       &exploit.CveID,
			Description: &exploit.Description,
			Name:        &exploit.Name,
			SourceDB:    &exploit.SourceDB,
			Title:       &exploit.Title,
			Urls:        &exploit.URLs,
		})
	}

	if retExploits == nil {
		return &models.ExploitScan{}
	}

	return &models.ExploitScan{
		Exploits: &retExploits,
	}
}

func (e *Exporter) ExportExploitsResult(res *results.Results, famerr families.RunErrors) error {
	scanResult, err := e.client.GetScanResult(context.TODO(), scanResultID, models.GetScanResultsScanResultIDParams{})
	if err != nil {
		return fmt.Errorf("failed to get scan result: %w", err)
	}

	if scanResult.Status == nil {
		scanResult.Status = &models.TargetScanStatus{}
	}
	if scanResult.Status.Exploits == nil {
		scanResult.Status.Exploits = &models.TargetScanState{}
	}
	if scanResult.Summary == nil {
		scanResult.Summary = &models.ScanFindingsSummary{}
	}

	errors := []string{}

	if err, ok := famerr[types.Exploits]; ok {
		errors = append(errors, err.Error())
	} else {
		exploitsResults, err := results.GetResult[*exploits.Results](res)
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to get exploits results from scan: %w", err).Error())
		} else {
			scanResult.Exploits = convertExploitsResultToAPIModel(exploitsResults)
			if scanResult.Exploits.Exploits != nil {
				scanResult.Summary.TotalExploits = utils.PointerTo(len(*scanResult.Exploits.Exploits))
			}
		}
	}

	state := models.DONE
	scanResult.Status.Exploits.State = &state
	scanResult.Status.Exploits.Errors = &errors

	err = e.client.PatchScanResult(context.TODO(), scanResult, scanResultID)
	if err != nil {
		return fmt.Errorf("failed to patch scan result: %w", err)
	}

	return nil
}

// nolint:cyclop
func (e *Exporter) ExportResults(res *results.Results, famerr families.RunErrors) []error {
	var errors []error

	if config.SBOM.Enabled {
		if err := e.ExportSbomResult(res, famerr); err != nil {
			errors = appendExportError("sbom", err, errors)
		}
	}

	if config.Vulnerabilities.Enabled {
		if err := e.ExportVulResult(res, famerr); err != nil {
			errors = appendExportError("vulnerabilties", err, errors)
		}
	}

	if config.Secrets.Enabled {
		if err := e.ExportSecretsResult(res, famerr); err != nil {
			errors = appendExportError("secrets", err, errors)
		}
	}

	if config.Exploits.Enabled {
		if err := e.ExportExploitsResult(res, famerr); err != nil {
			errors = appendExportError("exploits", err, errors)
		}
	}

	if config.Malware.Enabled {
		if err := e.ExportMalwareResult(res, famerr); err != nil {
			errors = appendExportError("malware", err, errors)
		}
	}

	return errors
}

func appendExportError(family string, err error, errors []error) []error {
	err = fmt.Errorf("failed to export %s result to server: %w", family, err)
	logger.Error(err)
	return append(errors, err)
}
