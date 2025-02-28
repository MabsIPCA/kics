package Cx

import data.generic.common as common_lib

resources := {"aws_s3_bucket_policy", "aws_s3_bucket"}

CxPolicy[result] {
	resourceType := resources[r]
	resource := input.document[i].resource[resourceType][name]

	not deny_http_requests(resource.policy)

	result := {
		"documentId": input.document[i].id,
		"searchKey": sprintf("%s[%s].policy", [resourceType, name]),
		"issueType": "IncorrectValue",
		"keyExpectedValue": sprintf("%s[%s].policy does not accept HTTP Requests", [resourceType, name]),
		"keyActualValue": sprintf("%s[%s].policy accepts HTTP Requests", [resourceType, name]),
		"searchLine": common_lib.build_search_line(["resource", resourceType, name, "policy"], []),
	}
}

CxPolicy[result] {
	module := input.document[i].module[name]
	resourceType := resources[r]
	keyToCheck := common_lib.get_module_equivalent_key("aws", module.source, resourceType, "policy")

	policy := module[keyToCheck]

	not deny_http_requests(policy)

	result := {
		"documentId": input.document[i].id,
		"searchKey": sprintf("module[%s].policy", [name]),
		"issueType": "IncorrectValue",
		"keyExpectedValue": "'policy' does not accept HTTP Requests",
		"keyActualValue": "'policy' accepts HTTP Requests",
		"searchLine": common_lib.build_search_line(["module", name, "policy"], []),
	}
}

any_s3_action(action) {
	any([action == "*", startswith(action, "s3:")])
}
check_action(st) {
	is_string(st.Action)
	any_s3_action(st.Action)
} else {
	any_s3_action(st.Action[a])
} else {
	is_string(st.Actions)
	any_s3_action(st.Actions)
} else {
	any_s3_action(st.Actions[a])
}

is_equal(secure, target)
{
    secure == target
}else {
    secure[_]==target
}

deny_http_requests(policyValue) {
    policy := common_lib.json_unmarshal(policyValue)
    st := common_lib.get_statement(policy)
    statement := st[_]
    check_action(statement)
    statement.Effect == "Deny"
    is_equal(statement.Condition.Bool["aws:SecureTransport"], "false")
} else {
    policy := common_lib.json_unmarshal(policyValue)
    st := common_lib.get_statement(policy)
    statement := st[_]
    check_action(statement)
    statement.Effect == "Allow"
    is_equal(statement.Condition.Bool["aws:SecureTransport"], "true")
}
