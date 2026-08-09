package recipe

import (
	"strings"
)

#EnvKey: =~"^[a-zA-Z_][a-zA-Z0-9_]*$"

#LXM_RECIPE_V1: close({
	schema?:   "lxm/recipe/v1"
	name?:     string & strings.MinRunes(1)
	run_as?:   string
	"run-as"?: string
	env?:      close({[#EnvKey]: string})
	sudo?:     bool | *false
	snapshot?: bool | *true
	retries?:  int & >=0 | *0
	scripts:   [...string] & [_, ...]
})
