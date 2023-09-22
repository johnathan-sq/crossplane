/*
Copyright 2023 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package xpkg

import metav1 "github.com/crossplane/crossplane/apis/pkg/meta/v1"

// InitContext defines the details we're interested in for populating a meta file.
type InitContext struct {
	// Name of the package
	Name string
	// Controller Image (only applicable to Provider packages)
	CtrlImage string
	// Crossplane version this package is compatible with
	XPVersion string
	// Dependencies for this package
	DependsOn []metav1.Dependency
}
