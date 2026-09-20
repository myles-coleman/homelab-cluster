# Homelab Overview dashboard (SigNoz v2 / Perses schema v6).
#
# Managed by Terragrunt. State is isolated per dashboard unit so a broken panel
# here cannot block the other dashboards. Metric names come from the verified
# inventory (see spec 01-spec-signoz-homelab-dashboards.md); grouping labels are
# `host.name` for host metrics and `k8s.*` for kubelet metrics.

resource "signoz_dashboard" "homelab_overview" {
  schema_version = "v6"
  name           = "homelab-overview"
  tags = [
    { key = "tag", value = "homelab" },
    { key = "tag", value = "overview" },
  ]

  spec = {
    display = {
      name        = "Homelab Overview"
      description = "At-a-glance health of the homelab: node pressure, host load, filesystem, namespace CPU, restarts and readiness."
    }
    links = []

    variables = [
      {
        list_variable = {
          kind = "ListVariable"
          spec = {
            display = {
              name        = "host_name"
              description = "Host sending node_exporter / host metrics"
            }
            allow_all_value = true
            allow_multiple  = true
            sort            = "alphabetical-asc"
            name            = "host_name"
            plugin = {
              dynamic_variable = {
                kind = "signoz/DynamicVariable"
                spec = {
                  name   = "host.name"
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
      },
    ]

    panels = {
      "node-cpu-usage" = {
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
      "node-memory-usage" = {
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
      "host-load-1m" = {
        kind = "Panel"
        spec = {
          display = {
            name        = "Host load average (1m)"
            description = "system.cpu.load_average.1m per host"
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
                            metric_name       = "system.cpu.load_average.1m"
                            time_aggregation  = "avg"
                            space_aggregation = "avg"
                            reduce_to         = "avg"
                          },
                        ]
                        filter = {
                          expression = "host.name IN $host_name"
                        }
                        group_by = [
                          {
                            name            = "host.name"
                            field_context   = "attribute"
                            field_data_type = "string"
                          },
                        ]
                        having = {
                          expression = ""
                        }
                        legend = "{{host.name}}"
                      }
                    }
                  }
                }
              }
            },
          ]
        }
      }
      "filesystem-usage" = {
        kind = "Panel"
        spec = {
          display = {
            name        = "Filesystem usage"
            description = "system.filesystem.usage per host"
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
                            metric_name       = "system.filesystem.usage"
                            time_aggregation  = "avg"
                            space_aggregation = "sum"
                            reduce_to         = "avg"
                          },
                        ]
                        filter = {
                          expression = "host.name IN $host_name"
                        }
                        group_by = [
                          {
                            name            = "host.name"
                            field_context   = "attribute"
                            field_data_type = "string"
                          },
                        ]
                        having = {
                          expression = ""
                        }
                        legend = "{{host.name}}"
                      }
                    }
                  }
                }
              }
            },
          ]
        }
      }
      "namespace-cpu" = {
        kind = "Panel"
        spec = {
          display = {
            name        = "Namespace CPU usage"
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
                  decimal_precision = "0"
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
      "nodes-ready" = {
        kind = "Panel"
        spec = {
          display = {
            name        = "Nodes ready"
            description = "k8s.node.condition_ready (min, last value)"
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
                  decimal_precision = "0"
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
                            metric_name       = "k8s.node.condition_ready"
                            time_aggregation  = "min"
                            space_aggregation = "min"
                            reduce_to         = "last"
                          },
                        ]
                        filter = {
                          expression = "k8s.node.name IN $k8s_node_name"
                        }
                        having = {
                          expression = ""
                        }
                        legend = "Ready nodes"
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
              title = "Nodes"
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
                  ref = "#/spec/panels/node-cpu-usage"
                }
              },
              {
                x      = 6
                y      = 0
                width  = 6
                height = 6
                content = {
                  ref = "#/spec/panels/node-memory-usage"
                }
              },
              {
                x      = 0
                y      = 6
                width  = 6
                height = 6
                content = {
                  ref = "#/spec/panels/host-load-1m"
                }
              },
              {
                x      = 6
                y      = 6
                width  = 6
                height = 6
                content = {
                  ref = "#/spec/panels/filesystem-usage"
                }
              },
              {
                x      = 0
                y      = 12
                width  = 12
                height = 6
                content = {
                  ref = "#/spec/panels/namespace-cpu"
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
              {
                x      = 6
                y      = 18
                width  = 6
                height = 6
                content = {
                  ref = "#/spec/panels/nodes-ready"
                }
              },
            ]
          }
        }
      },
    ]
  }
}
