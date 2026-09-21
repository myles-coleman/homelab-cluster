# Kubernetes & Nodes dashboard (SigNoz v2 / Perses schema v6).
#
# Managed by Terragrunt. State is isolated per dashboard unit so a broken panel
# here cannot block the other dashboards. Metric names come from the verified
# inventory (see spec 01-spec-signoz-homelab-dashboards.md).

resource "signoz_dashboard" "kubernetes_nodes" {
  schema_version = "v6"
  name           = "kubernetes-nodes"
  tags = [
    { key = "tag", value = "homelab" },
    { key = "tag", value = "kubernetes" },
  ]

  spec = {
    display = {
      name        = "Kubernetes & Nodes"
      description = "Node and workload health: node CPU/memory/filesystem, pod CPU/memory by namespace, deployment availability and container restarts."
    }
    links = []

    variables = [
      {
        list_variable = {
          kind = "ListVariable"
          spec = {
            display = {
              name        = "k8s_node_name"
              description = "Kubernetes node"
            }
            allow_all_value = true
            allow_multiple  = true
            sort            = "alphabetical-asc"
            name            = "k8s_node_name"
            plugin = {
              dynamic_variable = {
                kind = "signoz/DynamicVariable"
                spec = {
                  name   = "k8s.node.name"
                  signal = "metrics"
                }
              }
            }
          }
        }
      },
      {
        list_variable = {
          kind = "ListVariable"
          spec = {
            display = {
              name        = "namespace"
              description = "Kubernetes namespace"
            }
            allow_all_value = true
            allow_multiple  = true
            sort            = "alphabetical-asc"
            name            = "namespace"
            plugin = {
              dynamic_variable = {
                kind = "signoz/DynamicVariable"
                spec = {
                  name   = "k8s.namespace.name"
                  signal = "metrics"
                }
              }
            }
          }
        }
      }
    ]

    panels = {
      "node-cpu" = {
        kind = "Panel"
        spec = {
          display = {
            name        = "Node CPU usage"
            description = "k8s.node.cpu.usage per node"
          }
          links = []
          plugin = {
            time_series_panel = {
              kind = "signoz/TimeSeriesPanel"
              spec = {
                visualization = {
                  time_preference = "global_time"
                  fill_spans      = true
                }
                formatting = {
                  unit              = "short"
                  decimal_precision = "2"
                }
                chart_appearance = {
                  line_interpolation = "spline"
                  show_points        = false
                  line_style         = "solid"
                  fill_mode          = "solid"
                  span_gaps = {
                    fill_only_below = false
                    fill_less_than  = "0s"
                  }
                }
                axes = {
                  soft_min     = 0
                  is_log_scale = false
                }
                legend = {
                  position = "bottom"
                  mode     = "list"
                }
              }
            }
          }
          queries = [
            {
              kind = "time_series"
              spec = {
                name = "A"
                plugin = {
                  builder_query = {
                    kind = "signoz/BuilderQuery"
                    spec = {
                      metrics = {
                        name          = "A"
                        step_interval = "60"
                        signal        = "metrics"
                        aggregations = [
                          {
                            metric_name       = "k8s.node.cpu.usage"
                            time_aggregation  = "avg"
                            space_aggregation = "avg"
                            reduce_to         = "avg"
                          },
                        ]
                        filter = {
                          expression = "k8s.node.name IN $k8s_node_name"
                        }
                        group_by = [
                          {
                            name            = "k8s.node.name"
                            field_context   = "attribute"
                            field_data_type = "string"
                          },
                        ]
                        having = {
                          expression = ""
                        }
                        legend = "{{k8s.node.name}}"
                      }
                    }
                  }
                }
              }
            },
          ]
        }
      }
      "node-memory" = {
        kind = "Panel"
        spec = {
          display = {
            name        = "Node memory usage"
            description = "k8s.node.memory.usage per node"
          }
          links = []
          plugin = {
            time_series_panel = {
              kind = "signoz/TimeSeriesPanel"
              spec = {
                visualization = {
                  time_preference = "global_time"
                  fill_spans      = true
                }
                formatting = {
                  unit              = "bytes"
                  decimal_precision = "2"
                }
                chart_appearance = {
                  line_interpolation = "spline"
                  show_points        = false
                  line_style         = "solid"
                  fill_mode          = "solid"
                  span_gaps = {
                    fill_only_below = false
                    fill_less_than  = "0s"
                  }
                }
                axes = {
                  soft_min     = 0
                  is_log_scale = false
                }
                legend = {
                  position = "bottom"
                  mode     = "list"
                }
              }
            }
          }
          queries = [
            {
              kind = "time_series"
              spec = {
                name = "A"
                plugin = {
                  builder_query = {
                    kind = "signoz/BuilderQuery"
                    spec = {
                      metrics = {
                        name          = "A"
                        step_interval = "60"
                        signal        = "metrics"
                        aggregations = [
                          {
                            metric_name       = "k8s.node.memory.usage"
                            time_aggregation  = "avg"
                            space_aggregation = "avg"
                            reduce_to         = "avg"
                          },
                        ]
                        filter = {
                          expression = "k8s.node.name IN $k8s_node_name"
                        }
                        group_by = [
                          {
                            name            = "k8s.node.name"
                            field_context   = "attribute"
                            field_data_type = "string"
                          },
                        ]
                        having = {
                          expression = ""
                        }
                        legend = "{{k8s.node.name}}"
                      }
                    }
                  }
                }
              }
            },
          ]
        }
      }
      "node-filesystem" = {
        kind = "Panel"
        spec = {
          display = {
            name        = "Node filesystem usage"
            description = "k8s.node.filesystem.usage per node"
          }
          links = []
          plugin = {
            time_series_panel = {
              kind = "signoz/TimeSeriesPanel"
              spec = {
                visualization = {
                  time_preference = "global_time"
                  fill_spans      = true
                }
                formatting = {
                  unit              = "bytes"
                  decimal_precision = "2"
                }
                chart_appearance = {
                  line_interpolation = "spline"
                  show_points        = false
                  line_style         = "solid"
                  fill_mode          = "solid"
                  span_gaps = {
                    fill_only_below = false
                    fill_less_than  = "0s"
                  }
                }
                axes = {
                  soft_min     = 0
                  is_log_scale = false
                }
                legend = {
                  position = "bottom"
                  mode     = "list"
                }
              }
            }
          }
          queries = [
            {
              kind = "time_series"
              spec = {
                name = "A"
                plugin = {
                  builder_query = {
                    kind = "signoz/BuilderQuery"
                    spec = {
                      metrics = {
                        name          = "A"
                        step_interval = "60"
                        signal        = "metrics"
                        aggregations = [
                          {
                            metric_name       = "k8s.node.filesystem.usage"
                            time_aggregation  = "avg"
                            space_aggregation = "sum"
                            reduce_to         = "avg"
                          },
                        ]
                        filter = {
                          expression = "k8s.node.name IN $k8s_node_name"
                        }
                        group_by = [
                          {
                            name            = "k8s.node.name"
                            field_context   = "attribute"
                            field_data_type = "string"
                          },
                        ]
                        having = {
                          expression = ""
                        }
                        legend = "{{k8s.node.name}}"
                      }
                    }
                  }
                }
              }
            },
          ]
        }
      }
      "pod-cpu" = {
        kind = "Panel"
        spec = {
          display = {
            name        = "Pod CPU usage"
            description = "k8s.pod.cpu.usage grouped by namespace"
          }
          links = []
          plugin = {
            time_series_panel = {
              kind = "signoz/TimeSeriesPanel"
              spec = {
                visualization = {
                  time_preference = "global_time"
                  fill_spans      = true
                }
                formatting = {
                  unit              = "short"
                  decimal_precision = "2"
                }
                chart_appearance = {
                  line_interpolation = "spline"
                  show_points        = false
                  line_style         = "solid"
                  fill_mode          = "solid"
                  span_gaps = {
                    fill_only_below = false
                    fill_less_than  = "0s"
                  }
                }
                axes = {
                  soft_min     = 0
                  is_log_scale = false
                }
                legend = {
                  position = "bottom"
                  mode     = "list"
                }
              }
            }
          }
          queries = [
            {
              kind = "time_series"
              spec = {
                name = "A"
                plugin = {
                  builder_query = {
                    kind = "signoz/BuilderQuery"
                    spec = {
                      metrics = {
                        name          = "A"
                        step_interval = "60"
                        signal        = "metrics"
                        aggregations = [
                          {
                            metric_name       = "k8s.pod.cpu.usage"
                            time_aggregation  = "avg"
                            space_aggregation = "sum"
                            reduce_to         = "avg"
                          },
                        ]
                        filter = {
                          expression = "k8s.namespace.name IN $namespace"
                        }
                        group_by = [
                          {
                            name            = "k8s.namespace.name"
                            field_context   = "attribute"
                            field_data_type = "string"
                          },
                        ]
                        having = {
                          expression = ""
                        }
                        legend = "{{k8s.namespace.name}}"
                      }
                    }
                  }
                }
              }
            },
          ]
        }
      }
      "pod-memory" = {
        kind = "Panel"
        spec = {
          display = {
            name        = "Pod working set"
            description = "k8s.pod.memory.working_set grouped by namespace"
          }
          links = []
          plugin = {
            time_series_panel = {
              kind = "signoz/TimeSeriesPanel"
              spec = {
                visualization = {
                  time_preference = "global_time"
                  fill_spans      = true
                }
                formatting = {
                  unit              = "bytes"
                  decimal_precision = "2"
                }
                chart_appearance = {
                  line_interpolation = "spline"
                  show_points        = false
                  line_style         = "solid"
                  fill_mode          = "solid"
                  span_gaps = {
                    fill_only_below = false
                    fill_less_than  = "0s"
                  }
                }
                axes = {
                  soft_min     = 0
                  is_log_scale = false
                }
                legend = {
                  position = "bottom"
                  mode     = "list"
                }
              }
            }
          }
          queries = [
            {
              kind = "time_series"
              spec = {
                name = "A"
                plugin = {
                  builder_query = {
                    kind = "signoz/BuilderQuery"
                    spec = {
                      metrics = {
                        name          = "A"
                        step_interval = "60"
                        signal        = "metrics"
                        aggregations = [
                          {
                            metric_name       = "k8s.pod.memory.working_set"
                            time_aggregation  = "avg"
                            space_aggregation = "sum"
                            reduce_to         = "avg"
                          },
                        ]
                        filter = {
                          expression = "k8s.namespace.name IN $namespace"
                        }
                        group_by = [
                          {
                            name            = "k8s.namespace.name"
                            field_context   = "attribute"
                            field_data_type = "string"
                          },
                        ]
                        having = {
                          expression = ""
                        }
                        legend = "{{k8s.namespace.name}}"
                      }
                    }
                  }
                }
              }
            },
          ]
        }
      }
      "deployment-available" = {
        kind = "Panel"
        spec = {
          display = {
            name        = "Deployments available"
            description = "k8s.deployment.available grouped by namespace"
          }
          links = []
          plugin = {
            time_series_panel = {
              kind = "signoz/TimeSeriesPanel"
              spec = {
                visualization = {
                  time_preference = "global_time"
                  fill_spans      = true
                }
                formatting = {
                  unit              = "short"
                  decimal_precision = "2"
                }
                chart_appearance = {
                  line_interpolation = "spline"
                  show_points        = false
                  line_style         = "solid"
                  fill_mode          = "solid"
                  span_gaps = {
                    fill_only_below = false
                    fill_less_than  = "0s"
                  }
                }
                axes = {
                  soft_min     = 0
                  is_log_scale = false
                }
                legend = {
                  position = "bottom"
                  mode     = "list"
                }
              }
            }
          }
          queries = [
            {
              kind = "time_series"
              spec = {
                name = "A"
                plugin = {
                  builder_query = {
                    kind = "signoz/BuilderQuery"
                    spec = {
                      metrics = {
                        name          = "A"
                        step_interval = "60"
                        signal        = "metrics"
                        aggregations = [
                          {
                            metric_name       = "k8s.deployment.available"
                            time_aggregation  = "avg"
                            space_aggregation = "avg"
                            reduce_to         = "avg"
                          },
                        ]
                        filter = {
                          expression = "k8s.namespace.name IN $namespace"
                        }
                        group_by = [
                          {
                            name            = "k8s.namespace.name"
                            field_context   = "attribute"
                            field_data_type = "string"
                          },
                        ]
                        having = {
                          expression = ""
                        }
                        legend = "{{k8s.namespace.name}}"
                      }
                    }
                  }
                }
              }
            },
          ]
        }
      }
      "container-restarts" = {
        kind = "Panel"
        spec = {
          display = {
            name        = "Container restarts"
            description = "k8s.container.restarts (max, last value)"
          }
          links = []
          plugin = {
            number_panel = {
              kind = "signoz/NumberPanel"
              spec = {
                visualization = {
                  time_preference = "global_time"

                }
                formatting = {
                  unit              = "short"
                  decimal_precision = "2"
                }
              }
            }
          }
          queries = [
            {
              kind = "scalar"
              spec = {
                name = "A"
                plugin = {
                  builder_query = {
                    kind = "signoz/BuilderQuery"
                    spec = {
                      metrics = {
                        name          = "A"
                        step_interval = "60"
                        signal        = "metrics"
                        aggregations = [
                          {
                            metric_name       = "k8s.container.restarts"
                            time_aggregation  = "max"
                            space_aggregation = "max"
                            reduce_to         = "last"
                          },
                        ]
                        filter = {
                          expression = "k8s.namespace.name IN $namespace"
                        }
                        having = {
                          expression = ""
                        }
                        legend = "Restarts"
                      }
                    }
                  }
                }
              }
            },
          ]
        }
      }
    }

    layouts = [
      {
        grid = {
          kind = "Grid"
          spec = {
            display = {
              title = "Overview"
              collapse = {
                open = true
              }
            }
            items = [
              {
                x      = 0
                y      = 0
                width  = 6
                height = 6
                content = {
                  ref = "#/spec/panels/node-cpu"
                }
              },
              {
                x      = 6
                y      = 0
                width  = 6
                height = 6
                content = {
                  ref = "#/spec/panels/node-memory"
                }
              },
              {
                x      = 0
                y      = 6
                width  = 6
                height = 6
                content = {
                  ref = "#/spec/panels/node-filesystem"
                }
              },
              {
                x      = 6
                y      = 6
                width  = 6
                height = 6
                content = {
                  ref = "#/spec/panels/pod-cpu"
                }
              },
              {
                x      = 0
                y      = 12
                width  = 6
                height = 6
                content = {
                  ref = "#/spec/panels/pod-memory"
                }
              },
              {
                x      = 6
                y      = 12
                width  = 6
                height = 6
                content = {
                  ref = "#/spec/panels/deployment-available"
                }
              },
              {
                x      = 0
                y      = 18
                width  = 6
                height = 6
                content = {
                  ref = "#/spec/panels/container-restarts"
                }
              },
            ]
          }
        }
      },
    ]
  }
}
