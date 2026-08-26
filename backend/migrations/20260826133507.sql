-- Create "digger_plan_comment_groups" table
CREATE TABLE "public"."digger_plan_comment_groups" (
  "id" bigserial NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "deleted_at" timestamptz NULL,
  "batch_id" character varying(36) NULL,
  "group_index" bigint NULL,
  "comment_id" text NULL,
  "projects" bytea NULL,
  "rendered_job_count" bigint NULL,
  PRIMARY KEY ("id")
);
-- Create index "idx_digger_plan_comment_groups_deleted_at" to table: "digger_plan_comment_groups"
CREATE INDEX "idx_digger_plan_comment_groups_deleted_at" ON "public"."digger_plan_comment_groups" ("deleted_at");
-- Create index "idx_plan_comment_group_batch_index" to table: "digger_plan_comment_groups"
CREATE UNIQUE INDEX "idx_plan_comment_group_batch_index" ON "public"."digger_plan_comment_groups" ("batch_id", "group_index");
