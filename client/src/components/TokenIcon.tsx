import { displayAsset } from "@/lib/api";

const MARK: Record<string, string> = {
  USDC: "usdc.png",
  USDbC: "usdbc.png",
  EURC: "eurc.png",
  USDS: "usds.png",
  GHO: "gho.png",
  AERO: "aero.png",
  ETH: "eth.svg",
  cbETH: "cbeth.png",
  wstETH: "wsteth.png",
  weETH: "weeth.png",
  rETH: "reth.png",
  cbBTC: "cbbtc.png",
  tBTC: "tbtc.png",
};

const BRAND: Record<string, string> = {
  WBTC: "#F09242",
  DAI: "#F5AC37",
  USDT: "#26A17B",
};

export function TokenIcon({
  symbol,
  size = 24,
  className,
}: {
  symbol: string;
  size?: number;
  className?: string;
}) {
  const name = displayAsset(symbol);
  const file = MARK[name];

  if (file) {
    return (
      // eslint-disable-next-line @next/next/no-img-element
      <img
        src={`/tokens/${file}`}
        alt={name}
        width={size}
        height={size}
        className={`shrink-0 rounded-full ${className ?? ""}`}
        style={{ width: size, height: size }}
      />
    );
  }

  const brand = BRAND[name];
  const initial = (name.replace(/^[a-z]+/, "") || name).charAt(0).toUpperCase();
  return (
    <span
      aria-label={name}
      role="img"
      title={name}
      className={`inline-grid shrink-0 place-items-center rounded-full font-semibold ${
        brand ? "text-white" : "bg-secondary"
      } ${className ?? ""}`}
      style={{
        width: size,
        height: size,
        fontSize: Math.round(size * 0.42),
        ...(brand ? { backgroundColor: brand } : {}),
      }}
    >
      {initial || "?"}
    </span>
  );
}
