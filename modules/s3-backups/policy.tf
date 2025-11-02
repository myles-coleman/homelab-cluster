data "aws_iam_policy_document" "longhorn_backup_policy" {
  statement {
    sid    = "GrantLonghornBackupstoreAccess"
    effect = "Allow"
    
    actions = [
      "s3:PutObject",
      "s3:GetObject",
      "s3:ListBucket",
      "s3:DeleteObject"
    ]
    
    resources = [
      aws_s3_bucket.longhorn_backups.arn,
      "${aws_s3_bucket.longhorn_backups.arn}/*"
    ]
  }
}

resource "aws_iam_policy" "longhorn_backup_policy" {
  name        = "${var.cluster_name}-longhorn-backup-policy"
  description = "Policy for Longhorn backup access to S3"
  policy      = data.aws_iam_policy_document.longhorn_backup_policy.json

  tags = {
    Name        = "Longhorn Backup Policy"
    Environment = var.environment
    Cluster     = var.cluster_name
  }
}

resource "aws_iam_user" "longhorn_backup_user" {
  name = "${var.cluster_name}-longhorn-backup-user"
  path = "/"

  tags = {
    Name        = "Longhorn Backup User"
    Environment = var.environment
    Cluster     = var.cluster_name
  }
}

resource "aws_iam_user_policy_attachment" "longhorn_backup_policy_attachment" {
  user       = aws_iam_user.longhorn_backup_user.name
  policy_arn = aws_iam_policy.longhorn_backup_policy.arn
}

resource "aws_iam_access_key" "longhorn_backup_user_key" {
  user = aws_iam_user.longhorn_backup_user.name
}
