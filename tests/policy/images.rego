package main

import future.keywords.if
import future.keywords.in

# Rule 1: every container image ends in @sha256:<64 hex>.
#
# This is the twin of scripts/verify-dist.py's invariant, kept at the policy
# layer so a new service directory cannot bypass it by not being rendered yet,
# and so the CR-embedded Sandbox pod is covered too.

deny contains msg if {
	some document in input
	obj := document.contents
	spec := pod_spec(obj)
	some container in all_containers(spec)
	image := object.get(container, "image", "")
	image != ""
	not digest_pinned(image)
	msg := sprintf(
		"%s: %s/%s container %q has an unpinned image: %s",
		[document.path, obj.kind, name_of(obj), container_name(container), image],
	)
}

digest_pinned(image) if {
	regex.match("@sha256:[a-f0-9]{64}$", image)
}
