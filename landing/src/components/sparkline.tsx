import { cn } from "@/lib/utils";

const width = 96;
const height = 32;
// Half the stroke on each edge, so the line is never clipped by the viewBox.
const inset = 2;

/** A muted CPU line, drawn the way the console draws one on a database row. */
export function Sparkline({ points, className }: { points: number[]; className?: string }) {
  // A line needs two points; fewer would divide by zero below.
  if (points.length < 2) return null;

  const low = Math.min(...points);
  const span = Math.max(...points) - low || 1;
  const step = width / (points.length - 1);
  const line = points
    .map((value, index) => {
      const y = height - inset - ((value - low) / span) * (height - inset * 2);
      return `${(index * step).toFixed(2)},${y.toFixed(2)}`;
    })
    .join(" ");

  return (
    <svg
      viewBox={`0 0 ${width} ${height}`}
      preserveAspectRatio="none"
      className={cn("h-8 w-24 text-muted-foreground", className)}
      aria-hidden="true"
    >
      <polyline
        points={line}
        fill="none"
        stroke="currentColor"
        strokeWidth={1.5}
        strokeLinecap="round"
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  );
}
