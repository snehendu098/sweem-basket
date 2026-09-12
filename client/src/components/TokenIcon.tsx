import { displayAsset } from "@/lib/api";

/**
 * Token marks, bundled. Every file in public/tokens was fetched once from the
 * project's own asset repo — none of it is drawn here, and none of it is
 * hotlinked at runtime: a CDN <img> is a network request that fails into a
 * broken image.
 *
 * Provenance, resolved by Base mainnet contract address rather than by ticker,
 * because cbBTC/tBTC and cbETH/weETH/rETH are easy to swap by name:
 *
 *   trustwallet/assets  blockchains/base/assets/<addr>/logo.png
 *     usdc usdbc eurc usds aero cbeth reth
 *   trustwallet/assets  blockchains/ethereum/assets/<addr>/logo.png
 *     tbtc wsteth weeth gho   (no Base entry; same token, same mark)
 *   coingecko /coins/base/contract/0xcbb7…33bf -> images/40143
 *     cbbtc
 *   ethereum-optimism.github.io  data/ETH/logo.svg
 *     eth  (also what WETH renders as, see displayAsset)
 *
 * Anything unmapped keeps the monogram. A clean monogram is honest; a wrong
 * mark is not.
 */

/** display symbol -> file in public/tokens. Lowercase names, real extensions. */
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

/** Brand colours for the monogram disc. Colour only — never an invented shape. */
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
  /** The real API symbol, e.g. WETH. Display mapping happens here. */
  symbol: string;
  size?: number;
  className?: string;
}) {
  const name = displayAsset(symbol);
  const file = MARK[name];

  if (file) {
    return (
      // Plain <img>: these are fixed-size marks already at their final
      // dimensions, so the optimizer has nothing to do. rounded-full evens out
      // the few source files that ship on a square background.
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
  // Drop the wrapper prefix so cbBTC reads B and cbETH reads E, rather than
  // two identical C discs sitting next to each other in the token row.
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
