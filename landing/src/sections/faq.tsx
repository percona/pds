import { ChevronDown } from "lucide-react";
import { SectionHeading } from "@/components/section-heading";
import { sectionIds } from "@/lib/site";

const questions = [
  {
    q: "Where does my data live?",
    a: "On the EKS cluster the command creates in your own AWS account. PDS does not hold the database. Backups go to an S3 bucket in that account. A support ticket has connection strings, passwords and secrets removed before it is sent.",
  },
  {
    q: "What does PDS create in my AWS account?",
    a: "By default, an EKS cluster with four t3.2xlarge nodes in the region you choose, a NAT gateway, an S3 bucket for backups, and the roles that go with them. AWS bills those to your account, and each database adds a load balancer. The command asks for the region before it creates anything.",
  },
  {
    q: "What happens if I disconnect an environment?",
    a: "PDS stops managing it. The databases keep running, and nothing the join created in AWS is deleted. The agent on the Kubernetes cluster loses its credentials, so it can no longer act for PDS. The environment page lists what to remove, and how to keep the databases running on their own.",
  },
  {
    q: "What does alpha mean for my databases?",
    a: "There is no backward compatibility yet. Things change quickly, and you may need to recreate your setup from scratch. Run development and staging workloads, not data you cannot restore.",
  },
  {
    q: "How do we sign in?",
    a: "Open Sign in and use Google or GitHub. The first sign in creates your organization. There is no invitation to wait for.",
  },
  {
    q: "How do I get help?",
    a: "Use Contact support on the database that needs it. The ticket reaches Percona's engineers in ServiceNow with your database context attached, and with connection strings, passwords and secrets removed.",
  },
];

export function Faq() {
  return (
    <section id={sectionIds.faq} aria-labelledby="faq-title" className="px-6 py-24">
      <div className="mx-auto grid max-w-6xl gap-12 lg:grid-cols-[1fr_2fr]">
        <SectionHeading id="faq-title" eyebrow="FAQ" title="Questions">
          Sign in covers the console.
        </SectionHeading>

        <div className="divide-y divide-border border-y border-border">
          {questions.map(({ q, a }) => (
            <details key={q} className="group">
              <summary className="flex cursor-pointer list-none items-center justify-between gap-4 rounded-md py-4 text-sm font-medium [&::-webkit-details-marker]:hidden">
                {q}
                <ChevronDown
                  className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-180"
                  strokeWidth={1.5}
                  aria-hidden="true"
                />
              </summary>
              <p className="pb-5 text-sm text-muted-foreground">{a}</p>
            </details>
          ))}
        </div>
      </div>
    </section>
  );
}
