module github.com/florinel-chis/bvb-go/backtestsource

go 1.26.5

require (
	github.com/florinel-chis/bvb-go v0.0.0
	github.com/florinel-chis/gobacktest v0.1.1
)

// The parent module is not yet published; resolve it from the local checkout.
// A replace in a non-main module is ignored when this module is consumed as a
// dependency (the workbench supplies its own replace), so this only affects
// standalone development of bvb-go.
replace github.com/florinel-chis/bvb-go => ../
