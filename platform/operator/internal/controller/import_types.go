package controller

import flowv1 "github.com/gideas/flow/operator/api/v1"

const builtInLawPetitionImportType = "law-petition"

// EffectiveImportType describes one import type visible to Embassy resolution.
// Built-in import types are platform-owned; flow-authored types carry a spec
// from FoundryFlow.spec.crossFlow.importTypes.
type EffectiveImportType struct {
	Name    string
	BuiltIn bool
	Spec    *flowv1.ImportTypeSpec
}

func isBuiltInImportType(importType string) bool {
	_, ok := builtInImportTypes()[importType]
	return ok
}

func builtInImportTypes() map[string]EffectiveImportType {
	return map[string]EffectiveImportType{
		builtInLawPetitionImportType: {
			Name:    builtInLawPetitionImportType,
			BuiltIn: true,
		},
	}
}

func effectiveImportTypes(flow *flowv1.FoundryFlow) map[string]EffectiveImportType {
	effective := builtInImportTypes()
	if flow == nil || flow.Spec.CrossFlow == nil {
		return effective
	}

	for name, spec := range flow.Spec.CrossFlow.ImportTypes {
		specCopy := spec
		effective[name] = EffectiveImportType{
			Name: name,
			Spec: &specCopy,
		}
	}

	return effective
}
