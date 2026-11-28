package main

import future.keywords.if
import future.keywords.in

# Shared helpers for every rule in this directory.
#
# With `conftest test --combine`, `input` is an array of
# {"path": "...", "contents": {...}} rather than one document. Every rule below
# is written for that shape; a rule that indexes `input.contents` instead would
# match nothing and pass vacuously, which is why every rule ships a fail fixture.

workload_kinds := {
	"Deployment",
	"StatefulSet",
	"DaemonSet",
	"Job",
	"ReplicaSet",
	"ReplicationController",
}

# The pod spec is in a different place for each kind. CronJob nests it one level
# deeper; Sandbox hides it in a CR; Pod is the spec itself. Missing this split is
# how a policy ends up silently ignoring every CronJob.
pod_spec(obj) := spec if {
	obj.kind == "Pod"
	spec := object.get(obj, "spec", {})
}

pod_spec(obj) := spec if {
	obj.kind == "CronJob"
	job_template := object.get(object.get(obj, "spec", {}), "jobTemplate", {})
	template := object.get(object.get(job_template, "spec", {}), "template", {})
	spec := object.get(template, "spec", {})
}

pod_spec(obj) := spec if {
	obj.kind in workload_kinds
	template := object.get(object.get(obj, "spec", {}), "template", {})
	spec := object.get(template, "spec", {})
}

# The agents.x-k8s.io Sandbox carries its pod template in the CR. It is included
# because it is the pod a render-time rule would otherwise never see.
pod_spec(obj) := spec if {
	obj.kind == "Sandbox"
	template := object.get(object.get(obj, "spec", {}), "podTemplate", {})
	spec := object.get(template, "spec", {})
}

all_containers(spec) := containers if {
	containers := array.concat(
		array.concat(
			object.get(spec, "containers", []),
			object.get(spec, "initContainers", []),
		),
		object.get(spec, "ephemeralContainers", []),
	)
}

namespace_of(obj) := namespace if {
	namespace := object.get(object.get(obj, "metadata", {}), "namespace", "")
}

name_of(obj) := name if {
	name := object.get(object.get(obj, "metadata", {}), "name", "?")
}

container_name(container) := name if {
	name := object.get(container, "name", "?")
}
