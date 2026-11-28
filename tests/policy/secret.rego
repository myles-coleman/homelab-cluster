package main

import future.keywords.if
import future.keywords.in

# Rule 4: no populated Secret in the rendered tree.
#
# `manifests/dist/` is committed. A Secret with a plaintext `data` or
# `stringData` block would put the same value in the repository twice — once in
# the source and once in the generated tree — and make rotation a two-file edit.
# Secrets are expected to arrive as ExternalSecret references only.

deny contains msg if {
	some document in input
	document.contents.kind == "Secret"
	count(object.keys(object.get(document.contents, "data", {}))) > 0
	msg := sprintf("%s: Secret/%s has a populated data block", [document.path, name_of(document.contents)])
}

deny contains msg if {
	some document in input
	document.contents.kind == "Secret"
	count(object.keys(object.get(document.contents, "stringData", {}))) > 0
	msg := sprintf("%s: Secret/%s has a populated stringData block", [document.path, name_of(document.contents)])
}
