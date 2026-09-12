"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useSyncExternalStore,
} from "react";
import {
  CHAINS,
  DEFAULT_CHAIN_ID,
  activeChainId,
  chainInfo,
  setActiveChainId,
  subscribeChain,
  type ChainId,
  type ChainInfo,
} from "./api";

const KEY = "sweem.chain";

/** localStorage throws in private browsing and when storage is disabled. */
function stored(): ChainId | null {
  try {
    const raw = window.localStorage.getItem(KEY);
    const id = Number(raw) as ChainId;
    return CHAINS.some((c) => c.id === id) ? id : null;
  } catch {
    return null;
  }
}

function store(id: ChainId) {
  try {
    window.localStorage.setItem(KEY, String(id));
  } catch {
    // Not being able to remember the choice is not worth breaking the switch.
  }
}

export type ChainCtx = ChainInfo & {
  chainId: ChainId;
  setChainId: (id: ChainId) => void;
  chains: readonly ChainInfo[];
};

const Ctx = createContext<ChainCtx | null>(null);

export function useChain(): ChainCtx {
  const c = useContext(Ctx);
  if (!c) throw new Error("useChain outside ChainProvider");
  return c;
}

/**
 * The selected network, and the bridge that lets src/lib/api.ts read it
 * without every call site passing a chain down.
 *
 * The chain itself lives in api.ts as an external store, so a switch updates
 * what the next fetch sends and re-renders every reader from the same write.
 * The server snapshot is always DEFAULT_CHAIN_ID; the remembered choice is
 * applied after hydration, so there is nothing for React to disagree about.
 */
export function ChainProvider({ children }: { children: React.ReactNode }) {
  const chainId = useSyncExternalStore(
    subscribeChain,
    activeChainId,
    () => DEFAULT_CHAIN_ID,
  );

  useEffect(() => {
    const s = stored();
    if (s !== null) setActiveChainId(s);
  }, []);

  const setChainId = useCallback((id: ChainId) => {
    store(id);
    setActiveChainId(id);
  }, []);

  return (
    <Ctx.Provider
      value={{ ...chainInfo(chainId), chainId, setChainId, chains: CHAINS }}
    >
      {children}
    </Ctx.Provider>
  );
}
