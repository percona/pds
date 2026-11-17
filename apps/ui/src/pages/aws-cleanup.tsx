import { CircleAlert, TriangleAlert } from "lucide-react";
import { PageHeader } from "@/components/layout/page-header";
import { Skeleton } from "@/components/ui/skeleton";
import {
  CommandBlock,
  GuideRefusal,
  Mono,
  Step,
  deleteEnvironmentCommand,
  joinOwnershipTag,
  useGuideTarget,
} from "@/features/environments";

/**
 * How to delete what a join left in the customer's AWS account. Disconnecting
 * touches nothing in AWS, so this page is the only thing that tells them what
 * is still billing. It renders from the route alone because the environment is
 * usually gone from the environments list by the time anyone reads it.
 */
export function AwsCleanupPage() {
  const { name, region, regionUnknown, hubUrl, pending, problem } = useGuideTarget();

  if (problem) {
    return <GuideRefusal title="Delete AWS resources" problem={problem} />;
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="Delete AWS resources"
        description={`Disconnecting ${name} left everything its join created in your AWS account, still billing. One command deletes it.`}
      />

      <Step title="What the command deletes">
        <p className="text-sm text-muted-foreground">
          It finds what the join created by the tag <Mono>{joinOwnershipTag}</Mono> set to{" "}
          <Mono>{name}</Mono>: the EKS cluster and its node group, the backup bucket, the backup IAM
          user and the EBS CSI role. It also deletes the EBS volumes the EKS cluster left behind,
          found by the tag <Mono>kubernetes.io/cluster/{name}</Mono> set to <Mono>owned</Mono>, so a
          database added later is included. It deletes only volumes that are available, once the
          nodes are gone, and never one that is attached or tagged for another EKS cluster.
        </p>
        <p className="flex items-center gap-2 text-sm font-medium text-destructive">
          <TriangleAlert className="size-4" strokeWidth={1.5} aria-hidden="true" />
          <span>Irreversible</span>
        </p>
        <p className="text-sm text-muted-foreground">
          This deletes every database on {name} and every backup in its bucket, and nothing can
          bring them back. To keep a copy of the backups, the command prints how before it asks. It
          lists what it found and asks you to type the environment name before it deletes anything.
        </p>
        <p className="flex items-start gap-2 text-sm">
          <CircleAlert
            className="mt-0.5 size-4 shrink-0 text-warning"
            strokeWidth={1.5}
            aria-hidden="true"
          />
          <span>
            It deletes AWS resources only, and only what the join tagged. It refuses an EKS cluster
            without the tag, such as one you ran before the join, because the join did not create
            it. What a join added to such an EKS cluster is left for you to delete. A bucket the
            join could not tag is not found: the join warned when that happened. A bucket that was
            already there, or a user you supplied with <Mono>BACKUP_ACCESS_KEY_ID</Mono>, is not
            tagged and stays. A join with <Mono>--local</Mono> created nothing in AWS, so there is
            nothing here to delete for it.
          </span>
        </p>
      </Step>

      <Step n={1} title="Sign in to AWS">
        <p className="text-sm text-muted-foreground">
          Run the command in a terminal that has <Mono>aws</Mono> and <Mono>eksctl</Mono>, signed in
          with the AWS profile and credentials the join used: run <Mono>export AWS_PROFILE=</Mono>{" "}
          with its name first if you used one. It also uses <Mono>kubectl</Mono> when it is
          installed, to delete the databases and load balancers before the EKS cluster.
        </p>
      </Step>

      <Step n={2} title="Choose the region">
        <p className="text-sm text-muted-foreground">
          Type the region you answered at the join&apos;s prompt. If you pressed Enter there, the
          join used <Mono>AWS_REGION</Mono>, then <Mono>AWS_DEFAULT_REGION</Mono>, then your AWS
          configuration (<Mono>aws configure get region</Mono>)
          {hubUrl !== "" && !regionUnknown && !pending ? (
            <>
              , and <Mono>{region}</Mono> only when you had none
            </>
          ) : null}
          . The command never guesses it: a delete in the wrong region finds nothing. Run this block
          on its own: it waits for your answer.
        </p>
        <CommandBlock
          command={"printf 'Region you answered when you joined: '; read -r REGION; export REGION"}
        />
      </Step>

      <Step n={3} title="Run the command">
        <p className="text-sm text-muted-foreground">
          It lists what it found in your AWS account and asks you to type <Mono>{name}</Mono> before
          it deletes anything. When <Mono>kubectl</Mono> reaches the EKS cluster it deletes the
          databases and every load balancer first, and waits for the load balancers to go. When it
          cannot, it warns and carries on, and the EKS cluster is deleted anyway. If it stops, run
          the same command again: it finds what is left.
        </p>
        {pending ? (
          <Skeleton className="h-16 w-full" />
        ) : regionUnknown ? (
          <p className="text-sm text-destructive">
            Settings could not be read, so the command cannot be printed. Reload the page to try
            again.
          </p>
        ) : hubUrl === "" ? (
          <p className="text-sm text-muted-foreground">
            This control plane has no public address yet, so the command cannot be printed. Contact
            Percona support.
          </p>
        ) : (
          <CommandBlock command={deleteEnvironmentCommand({ hubUrl, name })} />
        )}
      </Step>

      <Step n={4} title="Disconnect the environment">
        <p className="text-sm text-muted-foreground">
          The command deletes AWS resources only, so <Mono>{name}</Mono> stays under Environments.
          If it is still listed, choose Disconnect on it and click Remove.
        </p>
      </Step>
    </div>
  );
}
