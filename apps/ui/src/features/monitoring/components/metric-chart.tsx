import { Area, AreaChart, ResponsiveContainer } from "recharts";
import { cn } from "@/lib/utils";
import { metricTextEquivalent } from "../format";
import type { MetricSeries } from "../types";

type MetricChartProps = {
  series: MetricSeries;
  /** True for the Overview minis, which get a shorter chart and no padding. */
  compact?: boolean;
  className?: string;
};

const compactHeight = 48;
const fullHeight = 160;

/**
 * One metric drawn as an area. The whole chart is one `img` with the current
 * value and the trend as its accessible name, because a shape is not a
 * reading. Nothing animates: a chart that draws itself in would be motion
 * outside the allowed table.
 */
export function MetricChart({ series, compact = false, className }: MetricChartProps) {
  const data = series.points.map((point) => ({ t: Date.parse(point.t), v: point.v }));

  return (
    <div
      role="img"
      aria-label={metricTextEquivalent(series)}
      className={cn("w-full text-primary", className)}
      style={{ height: compact ? compactHeight : fullHeight }}
    >
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart data={data} margin={{ top: 4, right: 0, bottom: 0, left: 0 }}>
          <Area
            type="monotone"
            dataKey="v"
            stroke="currentColor"
            strokeWidth={1.5}
            fill="currentColor"
            fillOpacity={0.12}
            isAnimationActive={false}
            dot={false}
            activeDot={false}
          />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}
