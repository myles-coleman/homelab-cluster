variable "bucket_name" {
  description = "Name of the S3 bucket for Longhorn backups"
  type        = string
}

variable "environment" {
  description = "Environment name (e.g., production, staging, development)"
  type        = string
  default     = "production"
}

variable "cluster_name" {
  description = "Name of the Kubernetes cluster"
  type        = string
}

variable "backup_retention_days" {
  description = "Number of days to retain backups before deletion"
  type        = number
  default     = 90
}

variable "aws_region" {
  description = "AWS region for the S3 bucket"
  type        = string
  default     = "us-west-1"
}
