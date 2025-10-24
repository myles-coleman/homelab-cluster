locals {
  common_inputs = yamldecode(file(find_in_parent_folders("common-inputs.yaml")))
}
