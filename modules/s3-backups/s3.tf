resource "aws_s3_bucket" "longhorn_backups" {
  bucket = var.bucket_name

  tags = {
    Name        = "Longhorn Backups"
    Environment = var.environment
    Purpose     = "kubernetes-longhorn-backups"
  }
}

resource "aws_s3_bucket_versioning" "longhorn_backups" {
  bucket = aws_s3_bucket.longhorn_backups.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "longhorn_backups" {
  bucket = aws_s3_bucket.longhorn_backups.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "longhorn_backups" {
  bucket = aws_s3_bucket.longhorn_backups.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "longhorn_backups" {
  bucket = aws_s3_bucket.longhorn_backups.id

  rule {
    id     = "longhorn_backup_lifecycle"
    status = "Enabled"

    # Apply to all objects in the bucket
    filter {
      prefix = ""
    }

    # REMOVED: expiration rule was deleting Longhorn backup blocks after 90 days.
    # Longhorn uses incremental backups with shared blocks across snapshots.
    # S3 lifecycle expiration is fundamentally incompatible — it deletes blocks
    # still referenced by current backups, silently corrupting them.
    # Longhorn manages its own block cleanup when backups are pruned via retain.

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}
