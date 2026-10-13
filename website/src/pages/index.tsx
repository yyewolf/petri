import type {ReactNode} from 'react';
import Link from '@docusaurus/Link';
import Layout from '@theme/Layout';
import Heading from '@theme/Heading';

type Feature = {
  title: string;
  body: ReactNode;
};

const features: Feature[] = [
  {
    title: 'The real API server',
    body: (
      <>
        Upstream kube-apiserver code runs in your test process: validation,
        defaulting, admission, RBAC and strategic merge patch behave as on a
        cluster. No binaries to download, no Docker, nothing on disk.
      </>
    ),
  },
  {
    title: 'Controllers and Nodes',
    body: (
      <>
        Namespaces really go away and owners cascade. Turn on the workload
        controllers, add simulated Nodes, and run kube-scheduler or place Pods
        yourself with gomock-style expectations.
      </>
    ),
  },
  {
    title: 'An envtest drop-in',
    body: (
      <>
        Keep your controller-runtime suites: swap the envtest import for
        petri&apos;s drop-in and they run against an in-process control plane
        with no setup step in CI.
      </>
    ),
  },
];

export default function Home(): ReactNode {
  return (
    <Layout description="A real Kubernetes control plane inside go test">
      <header className="hero hero--petri">
        <div className="container">
          <Heading as="h1" className="hero__title">
            petri
          </Heading>
          <p className="hero__subtitle">A real Kubernetes control plane inside go test.</p>
          <div className="buttons">
            <Link className="button button--primary button--lg" to="/docs/getting-started">
              Get started
            </Link>
            <Link className="button button--secondary button--lg" href="https://github.com/yyewolf/petri">
              GitHub
            </Link>
          </div>
        </div>
      </header>
      <main className="features">
        <div className="container">
          <div className="row">
            {features.map((f) => (
              <div key={f.title} className="col col--4">
                <Heading as="h3">{f.title}</Heading>
                <p>{f.body}</p>
              </div>
            ))}
          </div>
        </div>
      </main>
    </Layout>
  );
}
