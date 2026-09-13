import { cn } from "@/lib/utils";

const MARK: Record<string, string> = {
  "aave-v3": "aave-v3.png",
  "compound-v3": "compound-v3.png",
  "morpho-blue": "morpho-blue.svg",
  moonwell: "moonwell.png",
  uniswap: "uniswap.png",
};

export const protocolMark = (project: string): string | undefined =>
  MARK[project];

export function ProtocolIcon({
  project,
  size = 20,
  className,
}: {
  project: string;
  size?: number;
  className?: string;
}) {
  const file = MARK[project];
  if (!file) return null;
  return (
    // eslint-disable-next-line @next/next/no-img-element
    <img
      src={`/protocols/${file}`}
      alt={project}
      width={size}
      height={size}
      style={{ width: size, height: size }}
      className={cn("aspect-square shrink-0 rounded-lg object-contain", className)}
    />
  );
}

// Overlapping row, newest on the left. Each mark carries a ring in the toast's
// own colour so the overlap reads as depth rather than a smudge.
export function ProtocolStack({
  projects,
  size = 26,
}: {
  projects: readonly string[];
  size?: number;
}) {
  const marks = projects.filter((p) => MARK[p]);
  if (marks.length === 0) return null;
  return (
    <span className="inline-flex shrink-0 items-center">
      {marks.map((p, i) => (
        <ProtocolIcon
          key={p}
          project={p}
          size={size}
          className={cn("ring-2 ring-popover", i > 0 && "-ml-2")}
        />
      ))}
    </span>
  );
}
