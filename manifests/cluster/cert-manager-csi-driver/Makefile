# Makefile for templating Helm charts

# Find all values files (values.yaml, values-*.yaml)
values := $(wildcard values*.yaml)

# Target to generate helm-chart.yaml from helmfile and values
helm-chart.yaml: helmfile.yaml $(values)
	helmfile template > $@
