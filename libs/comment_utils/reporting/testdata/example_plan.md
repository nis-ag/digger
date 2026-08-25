<details><summary>plan for shared-services 2026-08-24 10:54:03 (UTC)</summary>
<details><summary>Plan output (<a href="https://eu-central-1.console.aws.amazon.com/s3/object/example-plans-bucket?region=eu-central-1&prefix=example-sharedservices-account-134-shared-services.tfplan.txt">full plan</a>)</summary>

```terraform
OpenTofu will perform the following actions:

  # module.opentaco.aws_ecs_service.opentaco will be updated in-place
  ~ resource "aws_ecs_service" "opentaco" {
        id                                 = "arn:aws:ecs:eu-central-1:123456789012:service/opentaco-cluster/opentaco-service"
        name                               = "opentaco-service"
        tags                               = {}
      ~ task_definition                    = "arn:aws:ecs:eu-central-1:123456789012:task-definition/opentaco-task:7" -> (known after apply)
        # (18 unchanged attributes hidden)

        # (5 unchanged blocks hidden)
    }

  # module.opentaco.aws_ecs_task_definition.opentaco must be replaced
-/+ resource "aws_ecs_task_definition" "opentaco" {
      ~ arn                      = "arn:aws:ecs:eu-central-1:123456789012:task-definition/opentaco-task:7" -> (known after apply)
      ~ arn_without_revision     = "arn:aws:ecs:eu-central-1:123456789012:task-definition/opentaco-task" -> (known after apply)
      ~ container_definitions    = (sensitive value) # forces replacement
      ~ enable_fault_injection   = false -> (known after apply)
      ~ id                       = "opentaco-task" -> (known after apply)
      ~ revision                 = 7 -> (known after apply)
      - tags                     = {} -> null
        # (10 unchanged attributes hidden)
    }

Plan: 1 to add, 1 to change, 1 to destroy.

Warning: Deprecated attribute

  on .terraform/modules/copebit_terraform_jumphost/data.tf line 47, in data "aws_iam_policy_document" "ssm_access_policy":
  47:       "arn:aws:s3:::${data.aws_region.current.id}/*",

The attribute "id" is deprecated. Refer to the provider documentation for
details.

(and 11 more similar warnings elsewhere)

─────────────────────────────────────────────────────────────────────────────
```
</details>

<details><summary>Terraform plan validation check (shared-services)</summary>
Terraform plan validation checks succeeded :white_check_mark:
</details>


<details><summary>Plan summary</summary>

|  CHANGE  |                      RESOURCE                      |
|----------|----------------------------------------------------|
| update   | `module.opentaco.aws_ecs_service.opentaco`         |
| recreate | `module.opentaco.aws_ecs_task_definition.opentaco` |

</details>


<details><summary>Instructions</summary>

▶️ To apply these changes, run the following command:

```bash
digger apply -p shared-services
```

⏩ To apply all changes in this PR:
```bash
digger apply
```

🚮 To unlock all projects in this PR:
```bash
digger unlock
```

</details>

</details>
