variable "bucket_name" {
  description = "Name of the S3 bucket for Longhorn backups"
  type        = string
}

variable "environment" {
  description = "Environment name (e.g., production, staging, development)"
  type        = string
}

variable "cluster_name" {
  description = "Name of the Kubernetes cluster"
  type        = string
}

variable "aws_region" {
  description = "AWS region for the S3 bucket"
  type        = string
}
