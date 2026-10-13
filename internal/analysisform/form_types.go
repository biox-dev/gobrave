package analysisform

// Form component type identifiers.
//
// These mirror the `type` field written by the frontend form builder
// (go-brave-ui, COMPONENT_IMPLEMENTATIONS / COMPONENT_META) and are used server
// side to branch on a form item's component kind. Keeping them as constants
// avoids typo-prone string literals being duplicated across packages.
const (
	// CollectedAssaySelect is the table picker over upstream results that projects
	// one form field per collected column.
	CollectedAssaySelect = "CollectedAssaySelect"
)
