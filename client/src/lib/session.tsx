"use client";

import {
  getAccessToken,
  PrivyProvider,
  usePrivy,
  useSigners,
  useCreateWallet,
  useExportWallet,
  type WalletWithMetadata,
} from "@privy-io/react-auth";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
} from "react";
import { ApiError, walletFetch, type Res } from "./api";
import type { Me } from "./types";

const APP_ID = process.env.NEXT_PUBLIC_PRIVY_APP_ID ?? "";
const SIGNER_ID = process.env.NEXT_PUBLIC_PRIVY_SIGNER_ID ?? "";
const POLICY_ID = process.env.NEXT_PUBLIC_PRIVY_POLICY_ID ?? "";

export type Session = {
  ready: boolean;
  authenticated: boolean;
  login: () => void;
  logout: () => Promise<void>;
  /** The Privy embedded wallet. Undefined until Privy creates one. */
  wallet: WalletWithMetadata | undefined;
  createWallet: () => Promise<void>;
  creatingWallet: boolean;
  /** Our backend's record of this user. Null until POST /v1/me succeeds. */
  me: Me | null;
  meError: string | null;
  syncing: boolean;
  refreshMe: () => Promise<void>;
  /** Opens Privy's export modal. The key is shown in Privy's iframe, never to us. */
  exportWallet: () => Promise<void>;
  delegate: () => Promise<void>;
  revoke: () => Promise<void>;
  delegating: boolean;
  delegationError: string | null;
  /** Configuration problem, not a user problem — surfaced instead of hidden. */
  signerConfigured: boolean;
  /** Authenticated call to the wallet service. Token is fetched fresh each time. */
  api: <T>(path: string, init?: RequestInit) => Promise<Res<T>>;
};

const Ctx = createContext<Session | null>(null);

export function useSession(): Session {
  const s = useContext(Ctx);
  if (!s) throw new Error("useSession outside SessionProvider");
  return s;
}

function embeddedWallet(
  accounts: readonly unknown[] | undefined,
): WalletWithMetadata | undefined {
  return (accounts as WalletWithMetadata[] | undefined)?.find(
    (a) =>
      a.type === "wallet" &&
      a.chainType === "ethereum" &&
      (a.walletClientType === "privy" || a.walletClientType === "privy-v2"),
  );
}

function msg(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

function SessionInner({ children }: { children: React.ReactNode }) {
  const { ready, authenticated, user, login, logout } = usePrivy();
  const { addSigners, removeSigners } = useSigners();
  const { createWallet } = useCreateWallet();
  const { exportWallet } = useExportWallet();

  const [me, setMe] = useState<Me | null>(null);
  const [meError, setMeError] = useState<string | null>(null);
  const [syncing, setSyncing] = useState(false);
  const [delegating, setDelegating] = useState(false);
  const [creatingWallet, setCreatingWallet] = useState(false);
  const [delegationError, setDelegationError] = useState<string | null>(null);

  const wallet = embeddedWallet(user?.linkedAccounts);

  // The module-level getAccessToken is stable, so `api` is too — data-loading
  // effects downstream do not re-run on every render.
  const api = useCallback(
    async <T,>(path: string, init?: RequestInit): Promise<Res<T>> => {
      // Privy refreshes the ~1h access token internally; ask for it per call
      // rather than caching one that will silently expire.
      const token = await getAccessToken();
      if (!token) throw new ApiError(401, "not logged in");
      return walletFetch<T>(path, token, init);
    },
    [],
  );

  const bind = useCallback(
    async (w: WalletWithMetadata) => {
      setSyncing(true);
      setMeError(null);
      try {
        const { data } = await api<Me>("/v1/me", {
          method: "POST",
          body: JSON.stringify({
            wallet_address: w.address,
            // Privy only exposes the server wallet ID once the wallet has a
            // signer, so this is empty before delegation. The backend needs it
            // to execute, which is why we re-POST after delegating.
            privy_wallet_id: w.id ?? "",
            delegated: w.delegated,
          }),
        });
        setMe(data);
      } catch (e) {
        setMeError(msg(e));
      } finally {
        setSyncing(false);
      }
    },
    [api],
  );

  // Re-bind whenever the wallet's identity or delegation state changes.
  const lastSynced = useRef<string | null>(null);
  useEffect(() => {
    if (!ready || !authenticated || !wallet) return;
    const sig = `${wallet.address}|${wallet.id ?? ""}|${wallet.delegated}`;
    if (lastSynced.current === sig) return;
    lastSynced.current = sig;
    void bind(wallet);
  }, [ready, authenticated, wallet, bind]);

  const doLogout = useCallback(async () => {
    lastSynced.current = null;
    setMe(null);
    setMeError(null);
    await logout();
  }, [logout]);

  const refreshMe = useCallback(async () => {
    if (!wallet) return;
    lastSynced.current = null;
    await bind(wallet);
  }, [wallet, bind]);

  const doCreateWallet = useCallback(async () => {
    setCreatingWallet(true);
    setDelegationError(null);
    try {
      await createWallet();
    } catch (e) {
      setDelegationError(msg(e));
    } finally {
      setCreatingWallet(false);
    }
  }, [createWallet]);

  const doExportWallet = useCallback(async () => {
    if (!wallet) return;
    await exportWallet({ address: wallet.address });
  }, [wallet, exportWallet]);

  const delegate = useCallback(async () => {
    if (!wallet) return;
    if (!SIGNER_ID) {
      setDelegationError(
        "NEXT_PUBLIC_PRIVY_SIGNER_ID is not set. Delegation needs the key quorum ID that holds the executor's authorization key.",
      );
      return;
    }
    setDelegating(true);
    setDelegationError(null);
    try {
      // Privy React SDK v3: useSigners().addSigners.
      // docs.privy.io/wallets/using-wallets/signers/add-signers
      await addSigners({
        address: wallet.address,
        signers: [
          { signerId: SIGNER_ID, policyIds: POLICY_ID ? [POLICY_ID] : [] },
        ],
      });
      // The `user` object updates and the sync effect re-POSTs /v1/me with
      // delegated: true and the now-present privy_wallet_id.
      lastSynced.current = null;
    } catch (e) {
      setDelegationError(msg(e));
    } finally {
      setDelegating(false);
    }
  }, [wallet, addSigners]);

  const revoke = useCallback(async () => {
    if (!wallet) return;
    setDelegating(true);
    setDelegationError(null);
    try {
      // Removes every signer on the wallet: only the user can transact after this.
      await removeSigners({ address: wallet.address });
      lastSynced.current = null;
    } catch (e) {
      setDelegationError(msg(e));
    } finally {
      setDelegating(false);
    }
  }, [wallet, removeSigners]);

  return (
    <Ctx.Provider
      value={{
        ready,
        authenticated,
        login,
        logout: doLogout,
        wallet,
        createWallet: doCreateWallet,
        creatingWallet,
        me,
        meError,
        syncing,
        refreshMe,
        exportWallet: doExportWallet,
        delegate,
        revoke,
        delegating,
        delegationError,
        signerConfigured: SIGNER_ID !== "",
        api,
      }}
    >
      {children}
    </Ctx.Provider>
  );
}

export function SessionProvider({ children }: { children: React.ReactNode }) {
  if (!APP_ID) {
    return (
      <div className="p-8 text-sm text-red-400">
        NEXT_PUBLIC_PRIVY_APP_ID is not set. Copy .env.example to .env.local.
      </div>
    );
  }
  return (
    <PrivyProvider
      appId={APP_ID}
      config={{
        loginMethods: ["email", "wallet"],
        appearance: {
          theme: "dark",
          // The brand lime, so Privy's modal matches the app it opens over.
          accentColor: "#c4f56a",
          landingHeader: "Sign in to sweem",
          loginMessage: "Your funds stay in your own wallet.",
          // appearance.walletList takes WalletListEntry values — verified in
          // node_modules/@privy-io/react-auth/dist/dts/types-B70mtFgn.d.ts
          // (PrivyClientConfig.appearance.walletList, WalletListEntry union).
          // Brave has no entry of its own: it injects an EVM provider, so
          // detected_ethereum_wallets is what surfaces it. Phantom is listed
          // under its own key and connects over its EVM provider because
          // walletChainType is ethereum-only, which is what Base Sepolia needs.
          walletList: [
            "detected_ethereum_wallets",
            "metamask",
            "phantom",
            "wallet_connect",
          ],
          walletChainType: "ethereum-only",
        },
        // Every user needs an embedded wallet: it is the account the protocol
        // routes for. Without one there is nothing to delegate.
        embeddedWallets: { ethereum: { createOnLogin: "all-users" } },
      }}
    >
      <SessionInner>{children}</SessionInner>
    </PrivyProvider>
  );
}

/** Loading state for one wallet-service GET. Refetches when `path` changes. */
export function useApi<T>(path: string | null) {
  const { api, authenticated, ready } = useSession();
  const [nonce, setNonce] = useState(0);
  const [state, setState] = useState<{
    key: string;
    data: T | null;
    error: string | null;
  }>({ key: "", data: null, error: null });

  // The request key doubles as the loading signal: while it differs from the
  // key the last response settled under, a request is in flight. No setState
  // during the effect body, so no cascading render.
  const key = path === null || !ready || !authenticated ? "" : `${nonce}:${path}`;

  useEffect(() => {
    if (!key || path === null) return;
    let live = true;
    api<T>(path)
      .then(({ data }) => live && setState({ key, data, error: null }))
      .catch((e) => live && setState({ key, data: null, error: msg(e) }));
    return () => {
      live = false;
    };
  }, [key, path, api]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);
  return {
    data: state.data,
    error: state.error,
    loading: key !== "" && state.key !== key,
    reload,
  };
}

/** Same, for the unauthenticated market-data service. `key` identifies the request. */
export function useAsync<T>(key: string, fn: () => Promise<T>) {
  const [state, setState] = useState<{
    key: string;
    data: T | null;
    error: string | null;
  }>({ key: "", data: null, error: null });

  useEffect(() => {
    let live = true;
    fn()
      .then((data) => live && setState({ key, data, error: null }))
      .catch((e) => live && setState({ key, data: null, error: msg(e) }));
    return () => {
      live = false;
    };
    // fn is recreated every render by design; the key is what identifies it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  return {
    data: state.data,
    error: state.error,
    loading: state.key !== key,
  };
}
