/** The one value that lets a connection act in every region. */
export const allRegions = "*";

/** The AWS regions an environment can be built in. */
export const awsRegions: readonly string[] = [
  "us-east-1",
  "us-east-2",
  "us-west-1",
  "us-west-2",
  "ca-central-1",
  "ca-west-1",
  "mx-central-1",
  "sa-east-1",
  "eu-central-1",
  "eu-central-2",
  "eu-west-1",
  "eu-west-2",
  "eu-west-3",
  "eu-north-1",
  "eu-south-1",
  "eu-south-2",
  "il-central-1",
  "me-central-1",
  "me-south-1",
  "af-south-1",
  "ap-east-1",
  "ap-south-1",
  "ap-south-2",
  "ap-southeast-1",
  "ap-southeast-2",
  "ap-southeast-3",
  "ap-southeast-4",
  "ap-southeast-5",
  "ap-northeast-1",
  "ap-northeast-2",
  "ap-northeast-3",
];

/** How a connection's regions read in the console. */
export function regionsLabel(regions: string[]): string {
  return regions.includes(allRegions) ? "All regions" : regions.join(", ");
}

/** The regions a new environment can be built in through a connection. */
export function regionChoices(regions: string[]): readonly string[] {
  return regions.includes(allRegions) ? awsRegions : regions;
}
