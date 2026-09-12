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
