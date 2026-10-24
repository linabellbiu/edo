package cicd

import "encoding/json"

func jsonValue(value any) string { data, _ := json.Marshal(value); return string(data) }
