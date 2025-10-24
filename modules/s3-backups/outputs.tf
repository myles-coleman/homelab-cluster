output "bucket_name" {
  description = "Name of the created S3 bucket"
  value       = aws_s3_bucket.longhorn_backups.id
}

output "bucket_arn" {
  description = "ARN of the created S3 bucket"
  value       = aws_s3_bucket.longhorn_backups.arn
}

output "bucket_domain_name" {
  description = "Domain name of the S3 bucket"
  value       = aws_s3_bucket.longhorn_backups.bucket_domain_name
}

output "iam_user_name" {
  description = "Name of the IAM user for Longhorn backups"
  value       = aws_iam_user.longhorn_backup_user.name
}

output "iam_user_arn" {
  description = "ARN of the IAM user for Longhorn backups"
  value       = aws_iam_user.longhorn_backup_user.arn
}

output "access_key_id" {
  description = "Access key ID for the Longhorn backup user"
  value       = aws_iam_access_key.longhorn_backup_user_key.id
}

output "secret_access_key" {
  description = "Secret access key for the Longhorn backup user"
  value       = aws_iam_access_key.longhorn_backup_user_key.secret
  sensitive   = true
}

output "backup_target_url" {
  description = "Longhorn backup target URL for S3"
  value       = "s3://${aws_s3_bucket.longhorn_backups.id}@${var.aws_region}/longhorn-backups/"
}
