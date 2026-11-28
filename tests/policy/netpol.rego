package main

import future.keywords.if
import future.keywords.in

# Rule 3: a namespace that contains a workload has at least one NetworkPolicy.
#
# This is a cross-document assertion, which is why the invocation needs
# `--combine`. It is weak by construction — a permissive policy satisfies it —
# and is kept because it catches the missing-file case cheaply.

deny contains msg if {
	some document in input
	obj := document.contents
	spec := pod_spec(obj)
	count(object.get(spec, "containers", [])) > 0
	namespace := namespace_of(obj)
	namespace != ""
	not has_network_policy(namespace)
	msg := sprintf("%s: namespace %q has workloads but no NetworkPolicy", [document.path, namespace])
}

has_network_policy(namespace) if {
	some document in input
	document.contents.kind == "NetworkPolicy"
	namespace_of(document.contents) == namespace
}
