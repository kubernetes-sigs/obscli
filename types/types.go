package types

import (
	"sigs.k8s.io/release-sdk/obs"
)

type Projects struct {
	Projects []Project `json:"projects"`
}

type Project struct {
	obs.Project
	RootProject string        `json:"rootProject,omitempty"`
	Packages    []obs.Package `json:"packages,omitempty"`
	Subprojects []Project     `json:"subprojects,omitempty"`
}
