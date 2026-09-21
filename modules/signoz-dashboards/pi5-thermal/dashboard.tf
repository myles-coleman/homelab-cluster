# Pi 5 Thermal dashboard (SigNoz v2 / Perses schema v6).
#
# Managed by Terragrunt. State is isolated per dashboard unit so a broken panel
# here cannot block the other dashboards. Metric names come from the verified
# inventory (see spec 01-spec-signoz-homelab-dashboards.md).

resource "signoz_dashboard" "pi5_thermal" {
  schema_version = "v6"
  name           = "pi5-thermal"
  tags = [
    { key = "tag", value = "homelab" },
    { key = "tag", value = "thermal" },
  ]

  spec = {
    display = {
      name        = "Pi 5 Thermal"
      description = "Raspberry Pi 5 thermal health: CPU temperature, CPU frequency and host load average per node."
    }
    links = []

    variables = [
      {
        list_variable = {
          kind = "ListVariable"
          spec = {
            display = {
              name        = "k8s_node_name"
              description = "Node (node_exporter relabel target)"
            }
            allow_all_value = true
            allow_multiple  = true
            sort            = "alphabetical-asc"
            name            = "k8s_node_name"
            plugin = {
              dynamic_variable = {
                kind = "signoz/DynamicVariable"
                spec = {
                  name   = "k8s_node_name"
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
              name        = "host_name"
              description = "Host sending host metrics"
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
      }
    ]

    panels = {
      "temperature" = {
        kind = "Panel"
        spec = {
          display = {
            name        = "CPU temperature"
            description = "node_thermal_zone_temp per node"
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
                  unit              = "celsius"
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
                thresholds = [
                  {
                    color = "#eab308"
                    value = 70
                    label = "Warm"
                    unit  = "celsius"
                  },
                  {
                    color = "#ef4444"
                    value = 80
                    label = "Hot"
                    unit  = "celsius"
                  },
                ]
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
                            metric_name       = "node_thermal_zone_temp"
                            time_aggregation  = "avg"
                            space_aggregation = "avg"
                            reduce_to         = "avg"
                          },
                        ]
                        filter = {
                          expression = "k8s_node_name IN $k8s_node_name"
                        }
                        group_by = [
                          {
                            name            = "k8s_node_name"
                            field_context   = "attribute"
                            field_data_type = "string"
                          },
                        ]
                        having = {
                          expression = ""
                        }
                        legend = "{{k8s_node_name}}"
                      }
                    }
                  }
                }
              }
            },
          ]
        }
      }
      "frequency" = {
        kind = "Panel"
        spec = {
          display = {
            name        = "CPU frequency"
            description = "node_cpu_frequency_hertz per node"
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
                  unit              = "hertz"
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
                            metric_name       = "node_cpu_frequency_hertz"
                            time_aggregation  = "avg"
                            space_aggregation = "avg"
                            reduce_to         = "avg"
                          },
                        ]
                        filter = {
                          expression = "k8s_node_name IN $k8s_node_name"
                        }
                        group_by = [
                          {
                            name            = "k8s_node_name"
                            field_context   = "attribute"
                            field_data_type = "string"
                          },
                        ]
                        having = {
                          expression = ""
                        }
                        legend = "{{k8s_node_name}}"
                      }
                    }
                  }
                }
              }
            },
          ]
        }
      }
      "load" = {
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
                width  = 12
                height = 6
                content = {
                  ref = "#/spec/panels/temperature"
                }
              },
              {
                x      = 0
                y      = 6
                width  = 6
                height = 6
                content = {
                  ref = "#/spec/panels/frequency"
                }
              },
              {
                x      = 6
                y      = 6
                width  = 6
                height = 6
                content = {
                  ref = "#/spec/panels/load"
                }
              },
            ]
          }
        }
      },
    ]
  }
}
