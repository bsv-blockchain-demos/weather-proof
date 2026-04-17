import { LandingHeader } from './LandingHeader';
import { HeroSection } from './HeroSection';
import { LiveStatsBar } from './LiveStatsBar';
import { ProblemSection } from './ProblemSection';
import { HowItWorksSection } from './HowItWorksSection';
import { ProofDemoCard } from './ProofDemoCard';
import { BenefitsSection } from './BenefitsSection';
import { FinalCTASection } from './FinalCTASection';
import { LandingFooter } from './LandingFooter';

export function LandingPage() {
  return (
    <div className="min-h-screen bg-storm-50 dark:bg-storm-950">
      <LandingHeader />
      <HeroSection />
      <LiveStatsBar />
      <ProblemSection />
      <HowItWorksSection />
      <ProofDemoCard />
      <BenefitsSection />
      <FinalCTASection />
      <LandingFooter />
    </div>
  );
}
