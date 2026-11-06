import { cn } from "@/lib/utils";

const width = 96;
const height = 40;
// Half the stroke on each edge, so the line is never clipped by the viewBox.
const inset = 2;

type CpuSparklineProps = {
  /** CPU percentages, oldest first. */
  points: number[];
  className?: string;
};

/**
 * The CPU line on a database row. Muted, 40px tall, and never the only carrier
 * of the reading: the accessible name states the current value and the trend as
 * text.
 */
export function CpuSparkline({ points, className }: CpuSparklineProps) {
  if (points.length < 2) {
    return null;
  }

  const current = points[points.length - 1] ?? 0;

  return (
    <svg
      role="img"
      aria-label={`CPU ${Math.round(current)}%, ${trend(points)}`}
      viewBox={`0 0 ${width} ${height}`}
      preserveAspectRatio="none"
      className={cn("h-10 w-24 text-muted-foreground", className)}
    >
      <polyline
        points={plot(points)}
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

/**
 * The series as viewBox coordinates. The scale is the range of the series
 * itself, so a flat line sits in the middle instead of on the floor.
 */
function plot(points: number[]): string {
  const low = Math.min(...points);
  const high = Math.max(...points);
  const span = high - low;
  const usable = height - inset * 2;
  const step = width / (points.length - 1);

  return points
    .map((value, index) => {
      const ratio = span === 0 ? 0.5 : (value - low) / span;
      const y = height - inset - ratio * usable;

      return `${(index * step).toFixed(2)},${y.toFixed(2)}`;
    })
    .join(" ");
}

function trend(points: number[]): string {
  const first = points[0] ?? 0;
  const last = points[points.length - 1] ?? 0;
  const change = last - first;

  if (Math.abs(change) < 1) {
    return "steady";
  }

  return change > 0 ? "trending up" : "trending down";
}
