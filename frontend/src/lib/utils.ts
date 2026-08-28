import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

// cn is the project-wide helper for merging Tailwind class strings. It
// lets component authors freely compose conditional classes while still
// letting consumer `class` attributes override internal defaults, because
// tailwind-merge understands Tailwind's "last-wins" semantics.
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
