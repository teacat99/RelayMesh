import { cva, type VariantProps } from 'class-variance-authority'

export const badgeVariants = cva(
  'inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs font-medium transition-colors focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2',
  {
    variants: {
      variant: {
        default:
          'border-transparent bg-primary/10 text-primary hover:bg-primary/15',
        secondary:
          'border-transparent bg-secondary text-secondary-foreground hover:bg-secondary/80',
        destructive:
          'border-transparent bg-destructive/10 text-destructive hover:bg-destructive/15',
        success:
          'border-transparent bg-[color:var(--color-status-active)]/10 text-[color:var(--color-status-active)]',
        warning:
          'border-transparent bg-[color:var(--color-status-pending)]/10 text-[color:var(--color-status-pending)]',
        muted:
          'border-transparent bg-muted text-muted-foreground',
        outline:
          'border-border text-foreground'
      }
    },
    defaultVariants: {
      variant: 'default'
    }
  }
)

export type BadgeVariants = VariantProps<typeof badgeVariants>
