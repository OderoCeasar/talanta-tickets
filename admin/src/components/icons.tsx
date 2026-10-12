import type { ReactNode } from "react";

function Icon({ children }: { children: ReactNode }) {
  return (
    <svg
      className="icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {children}
    </svg>
  );
}

export const HomeIcon = () => (
  <Icon>
    <path d="M4 11.5 12 4l8 7.5" />
    <path d="M6 10v9h12v-9" />
  </Icon>
);

export const CalendarIcon = () => (
  <Icon>
    <rect x="4" y="5.5" width="16" height="14" rx="2" />
    <path d="M4 10h16M8.5 3.5v4M15.5 3.5v4" />
  </Icon>
);

export const StadiumIcon = () => (
  <Icon>
    <ellipse cx="12" cy="12" rx="9" ry="6.5" />
    <ellipse cx="12" cy="12" rx="4.5" ry="2.75" />
  </Icon>
);

export const ChartIcon = () => (
  <Icon>
    <path d="M4 19.5h16" />
    <path d="M7 16v-4M12 16V7M17 16v-6.5" />
  </Icon>
);

export const TeamIcon = () => (
  <Icon>
    <circle cx="9" cy="8.5" r="3" />
    <path d="M3.5 19c.6-3 2.7-4.5 5.5-4.5s4.9 1.5 5.5 4.5" />
    <path d="M15.5 5.8a3 3 0 0 1 0 5.4M17.5 14.8c1.6.6 2.6 2 3 4.2" />
  </Icon>
);

export const UserIcon = () => (
  <Icon>
    <circle cx="12" cy="8.5" r="3.25" />
    <path d="M5.5 19.5c.7-3.2 3.2-5 6.5-5s5.8 1.8 6.5 5" />
  </Icon>
);

export const SignOutIcon = () => (
  <Icon>
    <path d="M10 4.5H6a1.5 1.5 0 0 0-1.5 1.5v12A1.5 1.5 0 0 0 6 19.5h4" />
    <path d="M15 8l4 4-4 4M19 12H9.5" />
  </Icon>
);

export const MenuIcon = () => (
  <Icon>
    <path d="M4 7h16M4 12h16M4 17h16" />
  </Icon>
);

export const CloseIcon = () => (
  <Icon>
    <path d="M6 6l12 12M18 6 6 18" />
  </Icon>
);

export const AlertIcon = () => (
  <Icon>
    <circle cx="12" cy="12" r="8.5" />
    <path d="M12 8v4.5M12 15.8v.2" />
  </Icon>
);

export const CheckIcon = () => (
  <Icon>
    <path d="m5 12.5 4.5 4.5L19 7.5" />
  </Icon>
);

export const CopyIcon = () => (
  <Icon>
    <rect x="8.5" y="8.5" width="11" height="11" rx="2" />
    <path d="M15.5 8.5V6a1.5 1.5 0 0 0-1.5-1.5H6A1.5 1.5 0 0 0 4.5 6v8A1.5 1.5 0 0 0 6 15.5h2.5" />
  </Icon>
);

export const MailIcon = () => (
  <Icon>
    <rect x="3.5" y="5.5" width="17" height="13" rx="2" />
    <path d="m4.5 7.5 7.5 5.5 7.5-5.5" />
  </Icon>
);

export const LinkOffIcon = () => (
  <Icon>
    <path d="M9.5 14.5 14.5 9.5" />
    <path d="M11 6.5 12.5 5a4.6 4.6 0 0 1 6.5 6.5L17.5 13M13 17.5 11.5 19A4.6 4.6 0 0 1 5 12.5L6.5 11" />
    <path d="M4 4l16 16" />
  </Icon>
);

export const ClockIcon = () => (
  <Icon>
    <circle cx="12" cy="12" r="8.5" />
    <path d="M12 7.5V12l3 2" />
  </Icon>
);

export const ShieldIcon = () => (
  <Icon>
    <path d="M12 3.5 5 6v5.5c0 4.2 2.8 7.3 7 9 4.2-1.7 7-4.8 7-9V6l-7-2.5Z" />
    <path d="m9 12 2.2 2.2L15.2 10" />
  </Icon>
);

export const BellIcon = () => (
  <Icon>
    <path d="M6.5 16.5v-5a5.5 5.5 0 0 1 11 0v5l1.5 2h-14l1.5-2Z" />
    <path d="M10 20.5a2.2 2.2 0 0 0 4 0" />
  </Icon>
);

/** Money going back: a coin with an arrow turning round it. */
export const RefundIcon = () => (
  <Icon>
    <path d="M4.5 12a7.5 7.5 0 1 0 2.4-5.5" />
    <path d="M4.5 4.5v3.2h3.2" />
    <path d="M12 8.5v7M14 10.2c-.4-.6-1.1-.9-2-.9-1.2 0-2 .6-2 1.4 0 2 4 .9 4 2.8 0 .8-.8 1.4-2 1.4-.9 0-1.7-.4-2.1-1" />
  </Icon>
);

/** The brand mark: a stadium bowl seen from above. */
export function BrandMark() {
  return (
    <svg className="brand__mark" viewBox="0 0 36 36" aria-hidden="true" focusable="false">
      <rect width="36" height="36" rx="9" fill="var(--champagne)" />
      <ellipse cx="18" cy="18" rx="12" ry="8.5" fill="none" stroke="var(--emerald-ink)" strokeWidth="2.2" />
      <ellipse cx="18" cy="18" rx="6" ry="3.6" fill="var(--emerald-ink)" />
    </svg>
  );
}
