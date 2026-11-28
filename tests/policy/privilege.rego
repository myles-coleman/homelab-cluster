package main

import future.keywords.if
import future.keywords.in

# Rule 2: no privileged containers, no explicit root, no privilege escalation.
#
# Carve-outs are named triples, never namespace-wide allowlists: an exception is
# a line a reviewer can see and grep. They match the rendered workload object,
# which is what this rule sees (a DaemonSet, not the pods it creates).

deny contains msg if {
	some document in input
	obj := document.contents
	spec := pod_spec(obj)
	some container in all_containers(spec)
	privileged(container)
	not excepted(obj)
	msg := sprintf(
		"%s: %s/%s container %q is privileged",
		[document.path, obj.kind, name_of(obj), container_name(container)],
	)
}

deny contains msg if {
	some document in input
	obj := document.contents
	spec := pod_spec(obj)
	some container in all_containers(spec)
	root(container, spec)
	not excepted(obj)
	msg := sprintf(
		"%s: %s/%s container %q runs as uid 0",
		[document.path, obj.kind, name_of(obj), container_name(container)],
	)
}

deny contains msg if {
	some document in input
	obj := document.contents
	spec := pod_spec(obj)
	some container in all_containers(spec)
	escalates(container)
	not excepted(obj)
	msg := sprintf(
		"%s: %s/%s container %q allows privilege escalation",
		[document.path, obj.kind, name_of(obj), container_name(container)],
	)
}

privileged(container) if {
	object.get(object.get(container, "securityContext", {}), "privileged", false) == true
}

root(container, spec) if {
	object.get(object.get(container, "securityContext", {}), "runAsUser", -1) == 0
}

root(container, spec) if {
	not object.get(object.get(container, "securityContext", {}), "runAsUser", null)
	object.get(object.get(spec, "securityContext", {}), "runAsUser", -1) == 0
}

escalates(container) if {
	object.get(object.get(container, "securityContext", {}), "allowPrivilegeEscalation", false) == true
}

# Enumerable exceptions, keyed on namespace + kind + name. Each one is a measured
# host requirement, not a convenience.
excepted(obj) if {
	[namespace_of(obj), obj.kind, name_of(obj)] == ["cert-manager", "DaemonSet", "cert-manager-csi-driver"]
}

excepted(obj) if {
	[namespace_of(obj), obj.kind, name_of(obj)] == ["longhorn-system", "DaemonSet", "longhorn-manager"]
}

excepted(obj) if {
	[namespace_of(obj), obj.kind, name_of(obj)] == ["longhorn-system", "DaemonSet", "longhorn-csi-plugin"]
}

excepted(obj) if {
	[namespace_of(obj), obj.kind, name_of(obj)] == ["node-exporter", "DaemonSet", "node-exporter"]
}
