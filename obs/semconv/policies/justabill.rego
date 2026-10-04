# Rules for the justabill registry, on top of Weaver's own checks (a brief on every name, a unit
# and instrument on every metric). Every name this registry defines must be under `justabill.`:
# upstream names are referenced with `ref`, never redefined, so a copy of an upstream convention
# (or a name outside our namespace) fails `weaver registry check`.
package before_resolution

import rego.v1

prefix := "justabill."

deny contains finding if {
	group := input.groups[_]
	attr := group.attributes[_]
	attr.id
	not startswith(attr.id, prefix)
	finding := violation(
		"attribute_outside_namespace",
		sprintf("Attribute '%s' in group '%s' must start with '%s'. Reference an upstream attribute with `ref` instead of defining it.", [attr.id, group.id, prefix]),
	)
}

deny contains finding if {
	group := input.groups[_]
	group.type == "metric"
	not startswith(group.metric_name, prefix)
	finding := violation(
		"metric_outside_namespace",
		sprintf("Metric '%s' in group '%s' must start with '%s'. Upstream metrics are imported, not redefined.", [group.metric_name, group.id, prefix]),
	)
}

deny contains finding if {
	group := input.groups[_]
	group.type == "event"
	not startswith(group.name, prefix)
	finding := violation(
		"event_outside_namespace",
		sprintf("Event '%s' in group '%s' must start with '%s'. Upstream events are imported, not redefined.", [group.name, group.id, prefix]),
	)
}

deny contains finding if {
	group := input.groups[_]
	not group.type in {"attribute_group", "metric", "event"}
	finding := violation(
		"unsupported_group_type",
		sprintf("Group '%s' has type '%s'; this registry defines only attributes, metrics and events.", [group.id, group.type]),
	)
}

violation(id, message) := {"id": id, "message": message, "level": "violation"}
